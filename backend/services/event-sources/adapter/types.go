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
type PresenceEvent struct {
	ExternalId string
	Connected  bool
	Reason     string
	SessionId  uint64
	OccurredAt time.Time
}
