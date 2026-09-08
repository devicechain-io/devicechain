// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package presence turns the NATS broker's own connection advisories into
// authoritative device presence (ADR-067) for devices that speak plain MQTT.
//
// Before this, a plain-MQTT device's presence was INFERRED — "it published within
// 600s" — which is a guess that alarms, dashboards and command delivery all inherit.
// The broker knows the answer exactly: it publishes a CONNECT and a DISCONNECT
// advisory into the system account for every connection in the APP account, carrying
// the MQTT client id, which the auth callout has already pinned to a device
// (device-management/processor/callout.go:230-247). Reading those turns the guess
// into an assertion, using the same StateChange event, the same stream and the same
// projection the Sparkplug and LwM2M sources already drive.
//
// 🔴 THE BROKER TELLS YOU WHEN A DEVICE DIES, EXCEPT WHEN IT DOESN'T. A graceful
// broker shutdown emits NO disconnect advisories for the connections it was holding —
// measured, not assumed — so every rolling broker upgrade leaves every connected
// device asserted-online with nothing to correct it, and an asserted row is skipped by
// the inactivity sweep (the presence_source predicate in Api.SweepInactive,
// device-state/model/api.go). That is why reconciliation
// against the broker's live inventory is part of this package rather than a follow-up:
// the advisories alone are not a complete account of a device's life.
package presence

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// advisory is the subset of a $SYS.ACCOUNT.<acct>.CONNECT / .DISCONNECT payload this
// package reads.
//
// 🔴 THE MQTT CLIENT ID IS `client_id` HERE AND `mqtt_client` IN A CONNZ REPLY. Both
// are filled from the same server-side call so the values always agree, but a decoder
// pointed at the wrong key reads "" — which is not an error, just a client id that
// matches no device. The path with the wrong key therefore reports an empty fleet,
// which is indistinguishable from an idle one. That is why this type and connzConn
// are separate types with separate tests, and why both are tested against raw broker
// JSON rather than a struct literal: a literal round-trips through our own tags and
// cannot see a wrong key at all.
type advisory struct {
	Reason string `json:"reason"`
	Client struct {
		MQTTClient string     `json:"client_id"`
		ClientType string     `json:"client_type"`
		Account    string     `json:"acc"`
		Start      *time.Time `json:"start"`
		Stop       *time.Time `json:"stop"`
	} `json:"client"`
}

// SkipReason names why an advisory produced no presence event. Every one of them is
// an ordinary occurrence rather than an error — the tap sees every connection the APP
// account admits, most of which are not devices — so they are counted by reason and
// not logged per event. The values are metric label values: keep them low-cardinality
// and derived from OUR classification, never from broker-supplied text.
type SkipReason string

const (
	// SkipNotADeviceConnection covers a connection with no MQTT client id at all:
	// every internal service, every operator tool, the tap's own SYS connection.
	SkipNotADeviceConnection SkipReason = "not_a_device_connection"
	// SkipNotThisInstance covers a well-formed device client id belonging to another
	// instance sharing the broker (ADR-048), and anything else that is not one of
	// our device ids.
	SkipNotThisInstance SkipReason = "not_this_instance"
	// SkipDiscriminatedSession covers a device's SECOND concurrent connection. See
	// TransitionFor for why these do not drive presence.
	SkipDiscriminatedSession SkipReason = "discriminated_session"
	// SkipNoConnectionTime covers an advisory with no usable start (or, for a death,
	// no usable stop). Fail-closed: a session id invented locally would agree with no
	// other surface, and a presence event is not worth guessing at.
	SkipNoConnectionTime SkipReason = "no_connection_time"
	// SkipMalformed covers a payload that is not the advisory JSON at all.
	SkipMalformed SkipReason = "malformed"
	// SkipCanary covers this service's own liveness probe. It is excluded AFTER the
	// parse rather than by presenting an unparseable id, because passing the parse is
	// most of what the canary exists to prove — see Canary.
	SkipCanary SkipReason = "canary"
)

// Transition is one advisory's meaning: which device, in which tenant, and the
// presence event to emit for it.
type Transition struct {
	Tenant      string
	DeviceToken string
	Event       adapter.PresenceEvent
}

// TransitionFor decides what a raw advisory means, with no I/O and no clock of its
// own — the whole classification, so it can be driven from recorded broker bytes. It
// returns either a Transition (skip == "") or the reason there is none.
//
// 🔑 ONLY THE PLAIN CLIENT ID DRIVES PRESENCE, and a discriminated one is skipped.
// A device may open extra concurrent connections by appending its own discriminator,
// which the callout admits (messaging.DeviceClientIDMatches) and which the docs
// encourage — a subscriber in one terminal, a publisher in another. The projection
// holds ONE (SessionId, Active) per device, so it cannot represent two live
// connections: the side connection's death would flip a device offline while its real
// session is still up, and under the delivery gate that holds its commands.
//
// Reference-counting the connections is not available as a fix. The advisories are
// consumed through a queue group, so a device's two edges routinely land on different
// replicas, and no replica ever sees a whole picture.
//
// So presence follows the PLAIN session only. That is well-defined because the broker
// enforces uniqueness on it: a second connect with the same plain id takes over and
// the loser is disconnected (measured, reason "Duplicate Client ID"). A device that
// only ever uses discriminated ids is never asserted, stays inferred, and the delivery
// gate publishes to it — the fail-open direction.
//
// The times are the BROKER's, never ours: `start` for a birth, `stop` for a death.
// Both edges of one connection are stamped by the one broker node that held it, so
// they are monotone by construction. A receipt clock would not be: the two edges land
// on different replicas, and a short-lived connection whose death is processed by a
// pod with a trailing clock would produce a death timestamped before its own birth,
// which presence.Decide rejects — leaving the device asserted-online forever.
func TransitionFor(instanceId string, connected bool, raw []byte) (Transition, SkipReason) {
	adv := advisory{}
	if err := json.Unmarshal(raw, &adv); err != nil {
		return Transition{}, SkipMalformed
	}
	if adv.Client.MQTTClient == "" {
		return Transition{}, SkipNotADeviceConnection
	}
	parts, err := messaging.ParseDeviceClientIDFor(instanceId, adv.Client.MQTTClient)
	if err != nil {
		return Transition{}, SkipNotThisInstance
	}
	if parts.Discriminator != "" {
		return Transition{}, SkipDiscriminatedSession
	}
	if IsCanary(parts) {
		// The canary opens a real MQTT connection under a device-shaped id, so the
		// broker announces it like any other. It must not become presence for a tenant
		// that does not exist.
		return Transition{}, SkipCanary
	}
	session, ok := SessionIDFrom(adv.Client.Start)
	if !ok {
		return Transition{}, SkipNoConnectionTime
	}
	occurred := *adv.Client.Start
	reason := "broker-connect"
	if !connected {
		// A death is stamped at its stop, not at the connection's start: the start is
		// the session's identity, the stop is when it ended. Refuse rather than
		// substitute — a death at the birth instant would be applied (Decide admits
		// DISCONNECTED at an equal stamp) but would record the wrong death time, which
		// is what the offline alarm and LastDisconnectTime report.
		if adv.Client.Stop == nil || adv.Client.Stop.IsZero() {
			return Transition{}, SkipNoConnectionTime
		}
		occurred = *adv.Client.Stop
		// The broker's own words for why the connection ended ("Client Closed",
		// "Duplicate Client ID", …). Carried through to the StateChange so an operator
		// can tell a device that hung up from one that was taken over. It reaches no
		// metric label — it is broker-supplied text.
		reason = adv.Reason
		if reason == "" {
			reason = "broker-disconnect"
		}
	}
	return Transition{
		Tenant:      parts.Tenant,
		DeviceToken: parts.DeviceToken,
		Event: adapter.PresenceEvent{
			Connected:  connected,
			Reason:     reason,
			SessionId:  session,
			OccurredAt: occurred.UTC(),
		},
	}, ""
}

// SessionIDFrom derives the presence SessionId from a connection's start instant.
//
// 🔑 THE CONNECTION'S START IS THE ONLY VALUE EVERY SURFACE AGREES ON. It appears
// byte-identical in the CONNECT advisory, in the DISCONNECT advisory for the same
// connection, and in the broker's CONNZ inventory (measured across all three), which
// is what lets a birth, a death and a reconciliation pass all name the same session.
// The alternatives do not have that property: the server-assigned cid is per-node and
// reissued across a cluster, and a local receipt time would differ per replica.
//
// It also makes a client-id takeover safe with no special handling: the loser's
// DISCONNECT carries the OLD connection's start, which is lower than the winner's, and
// presence.Decide rejects a lower session — so the surviving connection stays online.
//
// 🔴 Residual hazard, stated rather than solved: the start is stamped by whichever
// broker node the device landed on. A reconnect onto a node with a trailing clock
// yields a LOWER session id, which Decide rejects as stale — a connected device then
// reads dead. That is counted (see Metrics.RegressedSessions), not eliminated.
func SessionIDFrom(start *time.Time) (uint64, bool) {
	if start == nil || start.IsZero() {
		return 0, false
	}
	nanos := start.UnixNano()
	if nanos <= 0 {
		return 0, false
	}
	return uint64(nanos), true
}

// String makes a SkipReason usable as a metric label value without a conversion at
// every call site.
func (r SkipReason) String() string { return string(r) }

// describe renders a transition for logs without inventing a format at each site.
func (t Transition) describe() string {
	state := "DISCONNECTED"
	if t.Event.Connected {
		state = "CONNECTED"
	}
	return fmt.Sprintf("%s/%s %s session=%d", t.Tenant, t.DeviceToken, state, t.Event.SessionId)
}
