// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import "strconv"

// DedupID builds the JetStream "Nats-Msg-Id" for an inbound event from the
// capture stream sequence the event was decoded from, or "" when there is none
// (ADR-030 amendment, slice I2).
//
// The id is tenant-prefixed. JetStream dedup is STREAM-scoped and one
// inbound-events stream serves every tenant, so an id that is merely unique
// within a tenant lets one tenant's message suppress another's — the publish
// returns success, the message is acked, and the event is gone with nothing
// logged anywhere. The capture sequence is already unique across the whole
// capture stream, so the prefix is defence against a future second capture
// stream rather than load-bearing today; it costs nothing to keep.
//
// # What this id does and does not cover
//
// It identifies the CAPTURED MESSAGE, not the event. So it suppresses our own
// redelivery of a message we had already forwarded — the gap between "published
// to inbound-events" and "acked to the capture stream", which is what ADR-030
// exists to make safe. It does NOT suppress a DEVICE retransmitting after a
// missed PUBACK: that arrives as a genuinely new capture message with a new
// sequence, and is deliberately left to event-management's partial unique index
// on (tenant_id, alt_id, occurred_time).
//
// # Why the device's alternate id is NOT part of this key
//
// An earlier version of this preferred an `altId`-based id, on the reasoning that
// it identifies the event and so would also catch a device retransmit before it
// reached resolved-events (where the DETECT engine consumes on its own durable
// and does not dedup). Adversarial review killed it, and the reasons are recorded
// here because the idea is attractive enough to be proposed again:
//
//  1. It has no DEVICE in it, and cannot cheaply get one that agrees with the
//     rest of the platform. `altId` is device-chosen, and a per-device counter
//     ("1", "2", "seq-0001") is the normal firmware idiom, so two devices in one
//     tenant would silently destroy each other's telemetry. Confirmed on a real
//     broker: the second device's publish returned success with duplicate=true
//     and the stream held one message.
//  2. `altId` is device-controlled, unvalidated and unbounded, and the id is held
//     in BROKER MEMORY for the whole duplicate window. A single device sending a
//     60 KB alternate id turns the window into gigabytes of broker RSS.
//  3. NATS serializes headers through http.Header.Write, which rewrites CR and LF
//     as spaces. Two altIds that differ only by those characters are distinct Go
//     strings and the SAME id on the wire — so a device could aim suppression at
//     a specific victim event, and no Go-level test would see it.
//  4. Its precondition was unmeetable. The decoder defaults a missing occurred
//     time to time.Now(), so the altId form is only stable when the DEVICE
//     supplied the time — and Decode discards the parsed JsonEvent that knows,
//     returning an UnresolvedEvent whose OccurredTime is a bare time.Time. The
//     publish site could not tell the two apart, so the guard would have been
//     satisfied with a literal `true` and the id would have been built from a
//     regenerated timestamp that never matches anything.
//
// The capture sequence has none of those properties: it is ours, not the
// device's; it is a small fixed-size integer; and it is stable across a
// redelivery by construction. Catching device retransmits before DETECT is a real
// and open problem, but it needs to be solved where DETECT can see it, not by
// giving a device control over what our broker suppresses.
func DedupID(tenant string, captureSeq uint64) string {
	// A dedup id without a tenant prefix is the cross-tenant hazard above, and a
	// sequence of 0 means the message did not come from the capture stream at all
	// (the HTTP ingest path, which has no broker redelivery to dedup). Neither has
	// a safe fallback, so neither gets an id: no header is published, and the write
	// is simply not deduped. That is strictly safer than a weak id.
	if tenant == "" || captureSeq == 0 {
		return ""
	}
	return tenant + ":" + strconv.FormatUint(captureSeq, 10)
}
