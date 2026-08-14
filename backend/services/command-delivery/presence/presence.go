// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package presence answers one question for the delivery sweep: for each of these
// devices, is dispatching a command to it worth doing right now?
//
// It exists because a command is a PHYSICAL ACTUATION and, over MQTT, a publish reaches
// only a device that is connected and subscribed at that instant — the broker does not
// hold it. Dispatching to an absent device therefore does not merely fail; it fails
// SILENTLY, and command-delivery has already recorded the command as sent. The device is
// then blamed by a TIMEOUT for a command it was never given.
package presence

import (
	"context"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// Verdict is what the gate does with one command.
type Verdict int

const (
	// Dispatch means send it now. This is the DEFAULT and the fail-open direction:
	// the gate may only withhold on positive evidence of absence.
	Dispatch Verdict = iota
	// Hold means the device is authoritatively absent and can be expected back, so the
	// command waits for its return rather than being thrown at a broker that will drop it.
	Hold
	// Undeliverable means this device's transport cannot carry a command AT ALL, so
	// waiting would be a lie of a different kind — the device returns and delivery still
	// cannot happen.
	Undeliverable
)

// State is the projection's answer for one device, reduced to what the gate needs.
type State struct {
	Active   bool
	Asserted bool
	Source   string
	Known    bool // false when the projection has no row for this device
}

// Reader reads the live presence projection for a set of devices in one tenant.
type Reader interface {
	StatesFor(ctx context.Context, deviceTokens []string) (map[string]State, error)
}

// nonDeliveringTransports names the sources positively known to have NO command path at
// all. It is a DENY list, not the complement of an allow list, and that is deliberate: a
// source nobody has classified must fall through to Dispatch rather than be condemned by
// silence. The gate withholds only where the absence of a delivery path is established.
//
// 🔴🔴 THE ENTRY THAT MATTERS IS SPARKPLUG, AND IT IS NOT A BUG BEING WORKED AROUND.
// Sparkplug has no command egress by construction: nothing bridges the device-commands
// stream to a Sparkplug host, the host publishes only the Node Control/Rebirth control
// toward nodes, and its devices sit on the CUSTOMER's MQTT infrastructure rather than
// ours — so a publish onto our own subject reaches nothing.
//
// 🔑 A SPARKPLUG COMMAND IS THEREFORE NOT "HELD", IT IS UNDELIVERABLE, and that
// distinction is why this list exists at all. HELD means "waiting for the device to come
// back". For Sparkplug that is FALSE: the device does come back, on its next birth, and
// delivery still cannot happen. Holding would swap one lie for another — and it would
// occupy the tenant's undelivered-command ceiling for a full TTL, letting a Sparkplug
// fleet crowd out commands to devices that CAN receive them.
//
// 🔴🔴 KEYED ON THE TRANSPORT, MATCHED THROUGH transportOf — NEVER ON THE WHOLE `source`.
// A projected source is `transport` or `transport:qualifier`, and the qualifier is chosen at
// runtime, so the whole value is not a closed vocabulary and cannot be enumerated here. This
// list held the bare word "sparkplug" and was looked up with the whole value, which no
// producer ever writes: sparkplug-ingest stamps "sparkplug:"+hostId. The lookup therefore
// missed for every real device and the Undeliverable verdict — with failUndeliverable,
// MarkUndeliverable and command_delivery_undeliverable_total behind it — could not fire at
// all, while every test passed on a hand-written bare "sparkplug" that nothing emits.
var nonDeliveringTransports = map[string]bool{
	"sparkplug": true,
}

// transportOf reduces a projected `source` to the transport it names, dropping any
// runtime qualifier: "sparkplug:plant-a" -> "sparkplug", "mqtt" -> "mqtt".
//
// 🔴 IT IS NOT A PREFIX MATCH, AND THAT IS THE POINT. Matching by prefix would condemn a
// source merely NAMED like a denied transport — "sparkplugin", or an operator's gateway
// source called "sparkplug-test" — and `source` for a plain MQTT gateway is an
// OPERATOR-CHOSEN id, so those collisions are reachable input, not hypotheticals. Cutting
// at the first ":" matches the one form producers actually mint and nothing adjacent to it.
//
// An operator can still collide deliberately by naming a source exactly "sparkplug"; that
// is unchanged by this function and is the deny list's pre-existing shape, not a new hazard.
func transportOf(source string) string {
	transport, _, _ := strings.Cut(source, ":")
	return transport
}

// Decide reduces one device's projected state to a verdict.
//
// 🔴 THE `Asserted` CONJUNCT IS WHAT STOPS THIS BEING A DISASTER, AND IT IS NOT OBVIOUS.
// An INFERRED row's Active=false means only "no events for a while" — the data-silence
// sweep wrote it. That is not evidence the device cannot receive: a device reporting
// hourly is inactive for 59 minutes of every 60 and reachable throughout. Gating on
// !Active alone would hold every command to every quiet device on every transport that
// does not assert presence, which is most of them. Only an AUTHORITATIVE absence — a
// broker saying there is no connection — makes a dispatch provably futile.
//
// 🔴 AND THE EVIDENCE CAN BE STALE, which is stated here rather than discovered later.
// Two known windows leave a reachable device reading asserted-absent: a device whose plain
// broker session died while a discriminated side-connection stays subscribed, and a device
// that reconnected onto a broker node with a trailing clock (its lower session id is
// refused until the presence reconciler re-files it). In both, commands wait until that
// reconciler catches up. The gate withholds on positive evidence; the evidence is not
// infallible.
func Decide(s State) Verdict {
	if !s.Known {
		// No row at all: the platform has never been told anything about this device.
		// Dispatch, exactly as before the gate existed.
		return Dispatch
	}
	if nonDeliveringTransports[transportOf(s.Source)] {
		return Undeliverable
	}
	if s.Asserted && !s.Active {
		return Hold
	}
	return Dispatch
}

// graphqlReader reads the projection over device-state's GraphQL API.
type graphqlReader struct {
	client GraphQLClient
	url    string
}

// GraphQLClient is the svcclient seam, kept narrow so tests need no HTTP.
type GraphQLClient interface {
	Query(ctx context.Context, url, tenant, query string, vars map[string]any, out any) error
}

// NewGraphQLReader binds a reader to device-state's GraphQL endpoint.
func NewGraphQLReader(client GraphQLClient, url string) Reader {
	return &graphqlReader{client: client, url: url}
}

const statesQuery = `query($deviceTokens: [String!]!) {
  deviceStatesByDeviceToken(deviceTokens: $deviceTokens) {
    deviceToken
    active
    presenceSource
    source
  }
}`

// StatesFor reads the projection for a batch of this tenant's devices.
//
// 🔑 BATCHED ON PURPOSE. The sweep holds a whole tick's worth of queued commands, and one
// round trip per command would put a network call between the platform and every dispatch
// — turning a delivery sweep into a fan-out of reads. deviceStatesByDeviceToken is the
// only multi-token presence query available, so the batch is grouped per tenant.
//
// 🔴 A DEVICE MISSING FROM THE RESPONSE IS "UNKNOWN", NOT "ABSENT". The query returns rows
// that exist, so a device the projection has never seen simply does not come back. Reading
// that as absence would hold commands for every device that has not spoken yet.
func (r *graphqlReader) StatesFor(ctx context.Context, deviceTokens []string) (map[string]State, error) {
	out := make(map[string]State, len(deviceTokens))
	if len(deviceTokens) == 0 {
		return out, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("presence read requires a tenant in context")
	}
	var resp struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	}
	if err := r.client.Query(ctx, r.url, tenant, statesQuery,
		map[string]any{"deviceTokens": deviceTokens}, &resp); err != nil {
		return nil, err
	}
	for _, row := range resp.DeviceStatesByDeviceToken {
		out[row.DeviceToken] = State{
			Active:   row.Active,
			Asserted: row.PresenceSource == presenceSourceAsserted,
			Source:   row.Source,
			Known:    true,
		}
	}
	return out, nil
}

// presenceSourceAsserted mirrors device-state's constant. It is duplicated rather than
// imported because command-delivery does not otherwise depend on that module, and a
// one-word string is a cheaper coupling than a module edge — the value is part of the
// GraphQL contract, which is the thing actually being relied on.
const presenceSourceAsserted = "ASSERTED"
