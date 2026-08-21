// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package adapter is the protocol-neutral core of a standards-native edge-ingest
// service: it turns a source's decoded observations (numeric samples + connectivity
// transitions) into durable DeviceChain telemetry and presence, resolving — and
// optionally auto-registering — the device over the cross-service GraphQL client
// (ADR-044), and reconciling the device-state presence projection on failover
// (ADR-067). It carries NO protocol specifics: the Sparkplug session machine and the
// LwM2M CoAP/DTLS layer each decode their wire format into these neutral shapes and
// hand them to the shared Registrar/Emitter/Ingester/Reconciler. The two per-protocol
// identifiers that DO reach this layer — the device-token prefix and the dedup-id
// namespace prefix — are injected at construction, never hard-coded here.
package adapter

import "time"

// Sample is one resolved numeric metric ready to become a DeviceChain measurement: a
// name, a numeric value, and a timestamp in milliseconds since the Unix epoch. A
// source's decoder produces these; non-numeric metrics never become Samples —
// DeviceChain measurements are numeric by design (ADR-016).
type Sample struct {
	Name  string
	Value float64
	Time  int64
}

// PresenceEvent is an authoritative connectivity transition (ADR-067) a source's
// session or lifetime logic derives — a Sparkplug BIRTH/DEATH, an LwM2M
// register/lifetime-expiry. ExternalId is the DeviceChain external id of the device.
// SessionId is a host-observed connect epoch (the ADR-067 ordering key): a fresh
// connect mints a strictly-higher epoch so the projection can reject a stale
// transition. OccurredAt is the host RECEIPT clock, never a device-supplied payload
// timestamp — a death-signal (a Sparkplug will built at connect time, a lifetime timer
// firing) has no meaningful device timestamp and a payload ts could read as stale.
// DemotionEvent is a source RELEASING CUSTODY of a device (ADR-067): it will no longer
// speak for the device's connectivity, and it is asserting nothing about whether the
// device is up or down. The projection returns the row to INFERRED — which hands it back
// to the inactivity sweep and the implicit-heartbeat path, the two repair mechanisms an
// asserted row suppresses — and DETECT resolves any offline alarm it was holding, since
// the resolve would otherwise come from a CONNECT this source will never send.
//
// 🔴 THERE IS DELIBERATELY NO Connected FIELD, AND ITS ABSENCE IS THE TYPE'S WHOLE JOB.
// A demotion carried in a PresenceEvent would have to put SOMETHING in that bool, and
// whatever it put would be read downstream as a connectivity edge — false meaning a death
// nobody reported, and an offline alarm for every device a disabled source releases. The
// separate type makes the wrong emission unwritable rather than merely discouraged.
//
// SessionId names the session being released, and naming it is not bookkeeping: the
// projection accepts a demotion ONLY against the session it currently holds, so a
// demotion that names the wrong one is refused rather than misapplied. OccurredAt is the
// releasing clock's time. Reason is descriptive metadata for the audit trail, never an
// ordering or authorization input. DedupNonce follows PresenceEvent's rules below.
type DemotionEvent struct {
	SessionId  uint64
	OccurredAt time.Time
	Reason     string
	DedupNonce string
}

type PresenceEvent struct {
	ExternalId string
	Connected  bool
	Reason     string
	SessionId  uint64
	OccurredAt time.Time
	// DedupNonce, when set, joins the stream dedup key so this emission is distinct
	// from an otherwise identical one. Empty for anything a transport announced — those
	// SHOULD collapse, because a redelivery or a failover re-derivation of one advisory
	// is one event.
	//
	// 🔴 IT EXISTS FOR RECONCILIATION, WHOSE EMISSIONS ARE OBSERVATIONS RATHER THAN
	// RE-DERIVATIONS. The presence dedup key is (tenant, device, session, state) with no
	// time in it, which is right for advisories: a given session's connect and its
	// disconnect each happen once. A repair pass is different — it says "as of this pass,
	// the broker and the projection disagree", and after an intervening transition the
	// same (session, state) can be a genuinely NEW edge. Without a nonce the second one
	// is suppressed at the stream, the publish reports success because the PubAck is
	// discarded, and the repair counter records a repair that did not happen — for as
	// long as the duplicate window lasts.
	//
	// The cost is that N replicas reconciling the same divergence in one pass now write N
	// events instead of one. They are same-session same-state, so ordering marks every one
	// after the first as not-a-state-change and the projection does not move; the price is
	// rows, paid only for devices that are actually diverged.
	DedupNonce string

	// ExpectedSessionId is a compare-and-set precondition on the projection's stored
	// session, and it is the ONLY way a transition carrying a LOWER session id than the
	// projection holds is ever applied. Zero means "no claim", which is every transport
	// advisory: a transport reports what it observed and has not read the projection, so
	// it has nothing to compare against.
	//
	// 🔴 IT EXISTS BECAUSE SESSION IDS CAN GO BACKWARDS ACROSS BROKER NODES. The id is
	// minted from the wall clock of whichever node the device landed on, so a reconnect
	// onto a node whose clock trails its peers carries a genuinely-current id LOWER than
	// the stored one — and the ordinary "higher session wins" rule then rejects every
	// event about the live connection, permanently. Only a producer that has READ the
	// projection can break that tie, by naming what it read. See presence.Incoming.
	ExpectedSessionId uint64
}
