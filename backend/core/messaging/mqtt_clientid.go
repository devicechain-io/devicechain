// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// The MQTT client id a device connection must present.
//
// # Why the platform dictates it rather than accepting whatever firmware chose
//
// An MQTT client id is a SESSION KEY, not a display name. nats-server files a
// device's persistent session, its parked QoS 2 records and its PUBREL consumer
// under it, and MQTT's own semantics say a new connection presenting an existing
// client id TAKES OVER that session — the incumbent is disconnected and the
// arrival inherits its subscriptions. Left to firmware, that key is arbitrary
// text bound to no identity, which costs two things:
//
//   - A device in one tenant can present a client id in use by a device in
//     another and evict it. The callout-minted JWT still confines what the
//     arrival may publish or subscribe to (ADR-025), so it reads nothing — but
//     the incumbent is knocked off its connection and its session state is
//     destroyed. That is a cross-tenant denial of service reachable by any
//     device that can authenticate at all.
//   - Nothing the gateway files under that client id can be ATTRIBUTED to a
//     tenant afterwards, which is what left the ADR-077 broker purge unable to
//     reach session state it can see perfectly well.
//
// Requiring the id to start with "{instanceId}:{tenant}:{deviceToken}" closes the
// first outright: two devices cannot collide, because the prefix is derived from
// an identity the broker has already authenticated, and the check runs at the auth
// callout — which nats-server consults before it looks a session up, so a refusal
// prevents the takeover rather than undoing one.
//
// 🔴 IT ONLY PARTLY ADDRESSES THE SECOND, AND THE DIFFERENCE MATTERS. What the
// pin buys the purge is ATTRIBUTION — every id now names the tenant that owns it.
// It does NOT make the gateway's records reachable by a subject filter, and an
// earlier draft of this comment claimed it did. Both of the streams involved
// defeat that:
//
//   - $MQTT_sess files a session under "$MQTT.sess.{domainTk}{getHash(clientID)}"
//     — an 8-character digest of the id, not the id — so no filter can select a
//     tenant's sessions, and reaching one requires knowing the EXACT id.
//   - $MQTT_qos2in does carry the raw id, but as a single subject TOKEN, and NATS
//     wildcards match whole tokens only. "{instance}:{tenant}:*" is not a
//     subject filter, it is a string.
//
// So a purge has to recover the exact ids rather than compute them, which it can:
// the $MQTT_sess record's own payload carries its client id, so the ids are
// readable even though they are not filterable. That is what the pin makes
// possible and what it did not make cheap. Until that work lands,
// TenantPurgeDeferrals still names this state as surviving a purge.
//
// # 🔑 Why a PREFIX and not an exact value
//
// The obvious rule — the client id must EQUAL "{instanceId}:{tenant}:{deviceToken}"
// — gives a device exactly one client id, and therefore exactly one MQTT session.
// That sounds tidy and is wrong, because it makes a device's second connection
// evict its own first. Any firmware that publishes on one connection and
// subscribes for commands on another (a perfectly ordinary split, and what a
// reader following the docs does when they run mosquitto_sub in one terminal and
// mosquitto_pub in another) would knock itself offline — and nats-server punishes
// the retry: two clients trading one id inside a second land in its `flappers`
// jail and are held, then closed, so the failure presents as an unexplained
// connect loop.
//
// So a device MAY append its own discriminator after another separator. Nothing
// is given away by it: the suffix is chosen INSIDE a namespace the broker has
// already authenticated the device into, so it can still only ever collide with
// that same device's own sessions. A device evicting itself is a choice it made;
// a device evicting a stranger is what this exists to prevent.
//
// The cost, stated because the exact-match alternative would not have it: a device
// can mint unboundedly many sessions by cycling discriminators, and $MQTT_sess is
// deliberately unbounded (see mqtt_store.go), so each one leaves a record nothing
// reclaims. That is not a regression — before the pin ANY id was accepted, so the
// same device could do the same thing AND collide with other tenants while doing
// it — but it is a ceiling this rule chooses not to impose.

// deviceClientIDSeparator joins the fields of a device's MQTT client id.
//
// 🔑 IT IS ":" BECAUSE THE TOKEN GRAMMAR EXCLUDES IT, WHICH IS WHAT MAKES THE
// SPLIT UNAMBIGUOUS. ADR-042 admits letters, digits, "-" and "_" and nothing
// else, so a hyphen would not do: "acme-1:dev" and "acme:1-dev" are distinct,
// but "acme-1-dev" could be read either way, and a purge that guesses wrong
// either misses a tenant's sessions or deletes another tenant's. ":" is also
// what the connect USERNAME already uses to carry a tenant, for the same reason.
//
// It survives the trip: nats-server's own client-id validation rejects only
// whitespace and the NATS metacharacters "." "*" ">", so ":" is legal in a client
// id and — because it never appears in a token — the composed id is still a single
// NATS subject token wherever the gateway splices it into one.
const deviceClientIDSeparator = ":"

// DeviceClientID is the MQTT client id a device authenticating as deviceToken in
// tenant connects with when it wants one session — the plain form, and the one
// every first-party client and every documented example uses. A device wanting
// more than one appends ":" and a discriminator of its own; DeviceClientIDMatches
// is what the callout admits against.
//
// 🔑 THE INSTANCE ID IS A FIELD BECAUSE MQTT SESSIONS ARE KEYED PER ACCOUNT, NOT
// PER INSTANCE. Every other platform identifier is rooted at the instance —
// DeviceEventsSubject, StreamName, CacheBucketName — and on a shared broker
// (ADR-048) two instances share the single APP account, so an id without it would
// make "acme:sensor-001" in two different instances ONE session, and the
// cross-instance takeover would be the same defect this file removes, one level
// up. Carrying it costs a field now; adding it later would break the id every
// deployed device connects with.
//
// It refuses rather than composes when any field is not token-grammar-safe. That
// mirrors DevicePermissions, and for a related reason — a "." or a "*" would stop
// the id being one subject token, so the gateway would file the session somewhere
// the shape does not describe. Here it is belt-and-braces (the DB grammar guard
// should make it unreachable), but this is the value an erasure claim is computed
// from, and a claim resting on a distant invariant is a claim that fails silently
// when the invariant moves.
func DeviceClientID(instanceId, tenant, deviceToken string) (string, error) {
	for _, f := range []struct{ name, value string }{
		{"instance id", instanceId},
		{"tenant", tenant},
		{"device token", deviceToken},
	} {
		if err := core.ValidateToken(f.value); err != nil {
			return "", fmt.Errorf("refusing to build an MQTT client id for an invalid %s: %w", f.name, err)
		}
	}
	return instanceId + deviceClientIDSeparator + tenant + deviceClientIDSeparator + deviceToken, nil
}

// DeviceClientIDMatches reports whether a presented MQTT client id belongs to the
// device whose plain client id is required (built by DeviceClientID). It is the
// auth callout's admission test: the id is either exactly required, or required
// followed by the separator and a device-chosen discriminator.
//
// 🔴 THE SEPARATOR IN THE PREFIX TEST IS LOAD-BEARING, AND ITS ABSENCE IS A
// CROSS-DEVICE HOLE. A bare strings.HasPrefix(presented, required) would admit
// "…:sensor-0011" for device "sensor-001" — the id belonging to a DIFFERENT
// device whose token merely starts with this one's — and admitting it is exactly
// the takeover this file exists to prevent, now dressed as a discriminator.
// Requiring the separator makes the last field an exact match.
//
// This is the same equality-or-prefix-plus-separator test keyBelongsTo applies to
// KV keys, guarding a different hazard (cross-tenant data loss rather than
// cross-device takeover) with the same arithmetic. A third instance of the shape
// is worth extracting; two are worth cross-referencing.
func DeviceClientIDMatches(presented, required string) bool {
	return presented == required ||
		strings.HasPrefix(presented, required+deviceClientIDSeparator)
}
