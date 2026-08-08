// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// ThresholdOp is the DIRECTION a ThresholdAlarmRule's leaf comparison runs. The values are
// event-processing's rules.CompareOp literals, mirrored by hand for the reason everything
// in this module mirrors wire vocabulary (see wirevocabulary.go): the sim is deliberately
// an untrusted external client and cannot import the owning type.
//
// They live HERE rather than in wirevocabulary.go on that file's own stated rule — "declare
// it once where its consumers can reach it", not "put it in this file". The producer of
// these two strings is DefinitionJSON below, and it is their only consumer; there is no
// second reader for a shared file to serve. They still reach the authored-rules fixture,
// since they are rendered INTO the document that fixture publishes and event-processing's
// real compiler judges.
type ThresholdOp string

const (
	// OpGt fires while the metric runs ABOVE Threshold — an overheating sensor, a
	// pressure climbing. Every scenario predating this field meant this one.
	OpGt ThresholdOp = "gt"
	// OpLt fires while the metric runs BELOW Threshold — a DEPLETING quantity (fuel,
	// battery, tank level), where the interesting half of the cycle is the low one.
	OpLt ThresholdOp = "lt"
)

// validThresholdOps is the closed set DefinitionJSON admits. The zero value is
// deliberately NOT a member; see the panic in DefinitionJSON for why it must not be.
var validThresholdOps = map[ThresholdOp]bool{OpGt: true, OpLt: true}

// actionTypeRaiseAlarm / actionTypeSendCommand are event-processing's rules.ActionType
// discriminators. The wire envelope repeats the discriminator as the key its payload nests
// under ({"type":"sendCommand","sendCommand":{...}}), so each of these is spelled twice per
// action and a mismatch between the two halves is rejected at publish.
//
// Unexported, since nothing outside this package spells them — but shared across the
// package, because their two consumers carry OPPOSITE hazards. Rendering one wrong here is
// LOUD: the rule document decodes with DisallowUnknownFields, so the profile version fails
// to publish. Matching on one in Manifest.Validate's command cross-check is the exact
// wirevocabulary.go hazard — the non-matching branch is the PASSING one, so a renamed
// discriminator would quietly turn that check into a check of nothing while every test
// stayed green. One spelling makes the loud consumer the tripwire for the silent one.
const (
	actionTypeRaiseAlarm  = "raiseAlarm"
	actionTypeSendCommand = "sendCommand"
)

// ThresholdAlarmRule is a scenario's "alarm when this metric crosses that number" DETECT
// rule, declared as fields instead of hand-assembled JSON.
//
// 🔴 WHY THIS EXISTS AT ALL, GIVEN THE SIM MAY NOT IMPORT event-processing. The rule
// document is event-processing's rules.Rule, which decodes with DisallowUnknownFields —
// so a misspelled key is rejected at publish, and a rule that never compiles leaves the
// scenario's alarm widgets permanently empty. dc-simulator is deliberately an untrusted
// external client and cannot import that type, so the wire shape has to be written by
// hand SOMEWHERE. It does not have to be written by hand once per scenario. Every
// scenario that grew one of these copied the same map literal, the same json tags and
// the same panic; by the time buildingpulse gained a rule there were four copies of the
// grammar in this module, and a tag change meant four edits.
//
// Sharing the BUILDER is safe in a way sharing the CONSTANTS would not be. The
// authored-rules fixture's oracles all read the produced DOCUMENT — the tests decode
// this builder's output and hold it to each scenario's own constants, and
// event-processing runs that output through its real compiler — so a wrong shape from a
// shared builder still fails both. What must stay per-scenario is the curve, the
// threshold, the tokens: those are independent design decisions that happen to agree
// today (widgetlab's 15-35 triangle and buildingpulse's 16-32 sine both alarm at 30 by
// coincidence, not by a shared choice).
//
// The op WAS fixed at "gt" rather than being a field, on the argument that an inverted
// operator is the kind of thing that looks configured and silently alarms on the wrong half
// of the cycle. sitepulse genuinely needs "lt" (fuel running LOW), so it is a field now —
// and that argument survives intact as the field's REQUIREDNESS. There is no default and no
// zero value: an unset Op panics rather than rendering "gt", because a direction that
// defaults is precisely the silent-wrong-half failure the hardcoded value was protecting
// against, only now wearing a field the author believes they set.
type ThresholdAlarmRule struct {
	// Token is the rule's stable authoring id. It survives every definition change and
	// is what ruleHealth reports, so it must not encode anything about the predicate.
	Token string
	// Name is the human label shown in the console.
	Name string
	// Metric is the measurement key the predicate reads. It lands in BOTH the rule
	// document and the DetectionRuleSpec — see Spec, which is the whole point of this
	// type.
	Metric string
	// Op is which side of Threshold fires: OpGt above it, OpLt below it. REQUIRED — there
	// is no default, and DefinitionJSON panics on an unset or unknown value rather than
	// picking one. See the type comment: the safety argument for the operator having been
	// hardcoded was that a wrong direction looks configured, and a field that defaults
	// reopens exactly that.
	Op ThresholdOp
	// Threshold is the bound Op compares the metric against. BOTH directions are STRICT —
	// rules.CompareOp "gt" and "lt" are each exclusive — so a sample landing exactly on
	// this value does not fire either way, and a curve that only ever touches the
	// threshold produces no alarm at all in either direction.
	Threshold float64
	// Severity is the rule's AUTHORING severity, which is lowercase — see SeverityMajor
	// in wirevocabulary.go. The uppercase form is a different constant and belongs
	// nowhere near this field.
	Severity string
	// AlarmKey is what the raiseAlarm action keys its durable alarm on, per originator.
	AlarmKey string
	// CommandKey optionally chains a sendCommand action after the raiseAlarm, so a rule can
	// both ALARM and ACT — sitepulse's "fuel below 15% raises low-fuel AND sends the machine
	// to refuel". Empty renders exactly the single-action document every scenario published
	// before this field existed.
	//
	// 🔴 It is the profile CommandSpec.CommandKey — NOT that spec's Token and NOT its
	// display Name. All three are different strings on the same struct and all three are
	// grammar-valid tokens, so the two wrong ones publish perfectly clean: event-processing's
	// compiler is deliberately state-free and checks only the grammar, never whether any
	// profile declares the key. Nothing on the platform side notices until the rule FIRES,
	// at which point command-delivery's enqueue gate rejects the key, REACT classifies the
	// error as retryable, and the detection redelivers to the consumer's poison cap and
	// dead-letters — with nothing anywhere pointing back at the manifest line that wrote it.
	// Manifest.Validate closes that seam on this side; see ruleSendCommandKeys.
	//
	// ONE command, not a slice. A rule sending two commands is not a modelled case, and a
	// slice invites the failure that is: event-processing rejects an exact-duplicate action
	// at publish, so a [x, x] slice takes the whole profile version down with it.
	//
	// No payload field, because the modelled command is argument-free.
	// rules.SendCommandAction does carry an optional "payload" — a JSON OBJECT encoded as a
	// STRING — and the day a scenario needs arguments it belongs here as a second field
	// rendered under the same key.
	//
	// 🔴 MEASURED, because the obvious assumption about that field is wrong in the
	// dangerous direction. The publish gate reads `payload != "" && !isJSONObject(payload)`,
	// so a bare scalar/array/null IS rejected but an EMPTY STRING IS ACCEPTED — empty is
	// treated as absent, not as a malformed object. A payload field added here and then
	// rendered unconditionally would therefore publish a `"payload":""` that no gate
	// anywhere objects to, and the argument-free case would look configured. Render the key
	// only when there is an object to put in it, exactly as CommandKey gates its own action.
	CommandKey string
	// Enabled gates the rule at publish AND at load. A disabled rule is inert by design
	// (the publish gate does not submit it and the liveness check does not require it),
	// which also means it is published UNCHECKED — so a scenario parking one is
	// deliberately giving up the compiler's opinion of it.
	Enabled bool
}

// DefinitionJSON renders the opaque rules.Rule document the platform stores. The keys
// are exactly the rules.Rule / Condition / Action json tags.
func (r ThresholdAlarmRule) DefinitionJSON() string {
	if !validThresholdOps[r.Op] {
		// PANIC rather than an error return, and the reason both alternatives are worse
		// comes back to WHO can reach this line: only a scenario compiled into this binary.
		// Every registered scenario's manifest is built by a test in this package, so an
		// unset op is a failure every test run catches at once, not a runtime condition an
		// operator could ever hit. An error return would have to be threaded out through
		// Spec(), whose entire value is being usable inline in a composite literal at the
		// call site; and deferring the complaint to Manifest.Validate would mean rendering
		// SOMETHING first — and whatever that something was would be the silent default
		// this field exists to refuse.
		panic(fmt.Sprintf("sim: threshold rule %q has op %q, want %q or %q — the direction is "+
			"required and has no default, because a defaulted one alarms on the wrong half of "+
			"the cycle while looking configured", r.Token, r.Op, OpGt, OpLt))
	}

	// The action chain. Its ORDER carries no meaning to the platform — REACT dispatches
	// each action independently and keys idempotency on the action's CONTENT rather than
	// its list index, so reordering a chain is a no-op — but raiseAlarm is rendered first
	// regardless, so that a rule with no command produces the byte-identical document the
	// existing scenarios already publish to backend/testdata/authored-rules, which is
	// compared byte for byte.
	//
	// The two actions do NOT share edge behaviour, which is worth knowing when reading a
	// scenario that has both. On the RESOLVED (falling) edge REACT dispatches only the
	// raiseAlarm's structural clear; sendCommand has no falling-edge twin and is skipped,
	// since a command is a one-shot side effect and re-sending it when the condition CEASED
	// would double-fire it. For the motivating rule that is exactly the wanted behaviour —
	// fuel coming back up IS the refuel having worked, not a reason to dispatch again.
	actions := []any{
		map[string]any{
			"type":               actionTypeRaiseAlarm,
			actionTypeRaiseAlarm: map[string]any{"alarmKey": r.AlarmKey},
		},
	}
	if r.CommandKey != "" {
		actions = append(actions, map[string]any{
			"type":                actionTypeSendCommand,
			actionTypeSendCommand: map[string]any{"command": r.CommandKey},
		})
	}

	raw, err := json.Marshal(map[string]any{
		"name":     r.Name,
		"type":     "threshold",
		"severity": r.Severity,
		"when": map[string]any{
			"metric":    r.Metric,
			"op":        string(r.Op),
			"threshold": r.Threshold,
		},
		"actions": actions,
	})
	if err != nil {
		// Marshaling a static map of strings and floats cannot fail; a failure here is a
		// programming error, not a runtime condition.
		panic(fmt.Sprintf("sim: marshal threshold rule %q: %v", r.Token, err))
	}
	return string(raw)
}

// Spec renders the manifest entry.
//
// 🔑 THIS IS WHY THE TYPE EARNS ITS KEEP: DetectionRuleSpec.Metric and the document's
// own `when.metric` are two statements of one fact, and they used to be written
// separately at every call site. Manifest.Validate cross-checks them precisely because
// they could disagree — a rule DECLARING temperature while READING humidity passes every
// JSON check and never fires on the curve the scenario drives. Filling both from one
// field closes that seam by construction for anything built this way; Validate stays as
// the gate for rules built any other way.
func (r ThresholdAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{
		Token:      r.Token,
		Name:       r.Name,
		Definition: r.DefinitionJSON(),
		Metric:     r.Metric,
		Enabled:    r.Enabled,
	}
}
