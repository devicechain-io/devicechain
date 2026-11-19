// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

import "time"

// DeviceRosterEvent is the envelope emitted post-commit when a device is created or
// re-typed (ADR-051 slice 4c-2), telling event-processing that a device is EXPECTED
// to report so its DETECT engine can arm absence for a device that has NEVER reported
// (the reported-once-then-silent case is already covered by the heartbeat-armed timer).
//
// ProfileToken is the STABLE profile identity the device's type adopts (ADR-045) — not
// the "{profileToken}@{version}" token — so a re-publish that mints a new version never
// staleness-orphans the roster entry; event-processing cross-references the latest
// published version at arming time. It is empty when the type has no profile (a roster
// entry with no resolvable rules, retained so a later re-type re-homes it), and empty
// while the device is DISABLED: its events are refused at resolution, so it must arm no
// absence rule. An enable or a disable emits a fact of its own.
//
// ExpectedSince is when the device's membership of that profile began — its creation, or the
// stored instant of its last re-type or its type's last re-point (devices.expected_since) — the
// base of the dead-man clock for a never-reported device. The tenant is not a field — it travels on the per-tenant NATS
// subject, exactly like the detection-rules-published and entity-deleted facts.
type DeviceRosterEvent struct {
	DeviceToken   string
	ProfileToken  string
	ExpectedSince time.Time
	// Disabled is the explicit statement that an operator has the device disabled. ProfileToken is
	// empty for the same reason, and event-processing keys on that; Disabled exists so a consumer
	// that must tell "disabled" from "its type has no profile" (update-management) need not infer
	// it. It is additive on the wire: a consumer that predates it reads the field as false.
	Disabled bool
	// Enabled marks the fact an operator's disabled-to-enabled toggle emits, and no other: a create,
	// a re-type or a type re-point leave it false. A consumer that must catch a device up on enable
	// (update-management) acts on it alone; a producer that predates it sends false.
	Enabled bool
}
