// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

// Wire vocabularies the sim MIRRORS from platform services it cannot import.
//
// dc-simulator is deliberately an untrusted external client of the platform (see the
// package doc on main.go), so it speaks only the wire: every enum value below is a
// hand-copied literal whose authority lives in another module's Go type.
//
// 🔴 THE HAZARD IS NOT THAT A RENAME BREAKS THESE. It is that a rename leaves them
// SYNTACTICALLY FINE and semantically dead. Each of these appears in a comparison whose
// non-matching branch is the SUCCESSFUL one — an alarm widget filtering for active
// alarms, a load-test invariant asserting a safety probe never went active, a bootstrap
// post-assert requiring a rule to be running. Rename the value on the owning side and
// none of those error: the filter matches nothing and renders an empty table that reads
// as "no alarms yet", and the invariant becomes unfalsifiable and PASSES.
//
// So they are declared ONCE, here, rather than inline at each use — and published to
// backend/testdata/authored-rules by loadtest/authored_rules_fixture_test.go, where a
// test in each OWNING module holds them to the real enum. A second copy of any of these
// spelled inline somewhere is a copy no gate can see; that is exactly how the widgetlab
// alarm-count widget carried its own "ACTIVE" past every check in this arc.
const (
	// AlarmStateActiveWire / AlarmStateClearedWire are device-management's AlarmState
	// (its Alarm.state column is a plain string). Gated by device-management's
	// model/authored_sim_alarm_wire_test.go.
	AlarmStateActiveWire  = "ACTIVE"
	AlarmStateClearedWire = "CLEARED"

	// RuleStatusActiveWire is event-processing's RuleStatus for a detection rule that
	// compiled and is loaded in the engine — the one value a scenario's post-publish
	// liveness assert accepts. Gated by event-processing's
	// graphql/authored_sim_rules_test.go.
	RuleStatusActiveWire = "ACTIVE"
)
