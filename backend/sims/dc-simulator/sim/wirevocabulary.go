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
// So the values BOTH this package and loadtest need are declared once, here, rather than
// inline at each use — and published to backend/testdata/authored-rules by
// loadtest/authored_rules_fixture_test.go, where a test in each OWNING module holds them
// to the real enum. A second copy spelled inline somewhere is a copy no gate can see;
// that is exactly how the widgetlab alarm-count widget carried its own "ACTIVE" past
// every check in this arc.
//
// This is not the whole mirrored vocabulary. The load-test command statuses
// (QUEUED/SENT/SUCCESSFUL) stay in loadtest/command.go because nothing in this package
// needs them — they are published to the same fixture and gated by command-delivery just
// the same. The rule for a new mirrored value is "declare it once where its consumers can
// reach it, and put it in the fixture", not "put it in this file".
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

	// 🔴 THE ALARM TIER IS TWO CONSTANTS, AND THEY ARE NOT INTERCHANGEABLE — this is the
	// one entry here whose hazard is a rename of NEITHER side.
	//
	// SeverityMajor is the RULE AUTHORING vocabulary, which event-processing spells in
	// lowercase (its rules.Severity) and which its compiler rejects in any other case.
	// AlarmSeverityMajorWire is the SAME ADR-041 tier as it lands on the durable alarm
	// row, because the raise-alarm consumer uppercases the authoring severity — and it
	// is the form an alarm widget's `severity` filter option is written in.
	//
	// Collapsing them into one uppercase constant makes the profile UNPUBLISHABLE (the
	// rule compiler refuses "MAJOR"); collapsing them into one lowercase constant
	// publishes fine and leaves every alarm widget's severity filter matching nothing.
	// The second is the dangerous direction, and it is the mistake that actually
	// shipped once on the load-test harness. Note that a gate comparing the two to EACH
	// OTHER catches neither, since ToUpper("MAJOR") is "MAJOR" — which is why the real
	// check is the fixture, where event-processing judges the authoring form and
	// device-management judges the wire form, each against its own real type.
	//
	// Declared once here rather than per scenario: the scenarios' TIER CHOICE is a
	// design decision, but the PAIRING is a platform fact, and three copies of it meant
	// three copies of this warning. A scenario wanting a different tier adds that pair
	// here, where the fixture can reach it. (The load-test harness keeps its own pair in
	// loadtest/detection.go — its choice of major is arbitrary where a demo's is not,
	// and its comment carries oracle-specific reasoning about the INDETERMINATE
	// tombstone row that does not belong to any scenario.)
	SeverityMajor          = "major"
	AlarmSeverityMajorWire = "MAJOR"
)
