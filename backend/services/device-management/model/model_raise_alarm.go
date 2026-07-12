// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"time"
)

// AlarmEdgeRaised / AlarmEdgeResolved are the canonical RaiseAlarmRequest.Edge tokens (ADR-057) —
// the SHARED wire vocabulary both the producer (event-processing's alarmClient) and this consumer bind
// to. They live here, in the wire struct's own package, because it is the one package both modules
// import (event-processing imports dc-device-management/model; it cannot import event-processing's
// internal/runtime across the module boundary). A hardcoded literal on either side would drift
// FAIL-OPEN — a mis-spelled resolved token falls through to the raise path — so both sides reference
// these constants and the producer maps its internal edge onto them explicitly.
const (
	AlarmEdgeRaised   = "raised"
	AlarmEdgeResolved = "resolved"
)

// RaiseAlarmRequest is the raise-alarm message event-processing's REACT dispatcher sends to
// device-management (ADR-051 slice 5c / ADR-054) when a detection rule's raiseAlarm action fires.
// device-management raises/escalates the alarm through its existing engine (raiseOrEscalateAlarm),
// keeping the Alarm object, ack/clear, graph rollup, and alarm-events→notification last mile all in
// one place (ADR-041/017).
//
// It is a JSON message — like event-processing's derived events (ADR-037), and unlike
// device-management's protobuf FACT streams — because it is an event-processing-produced request
// this service consumes, not one of this service's own emitted facts; keeping it JSON avoids
// regenerating the protobuf bundle for a cross-service request. The producer marshals it via
// MarshalRaiseAlarmRequest; this service's consumer decodes it via UnmarshalRaiseAlarmRequest.
type RaiseAlarmRequest struct {
	// DeviceToken is the target device (the detection's series). The consumer resolves it to the
	// device row id raiseOrEscalateAlarm needs.
	DeviceToken string `json:"deviceToken"`
	// AlarmKey is the (originator, key) the alarm is keyed on (ADR-041 dec 3): repeated firings of
	// the same rule escalate ONE alarm in place. The dispatcher defaults an empty authored key to
	// the rule id, so this is always non-empty on the wire.
	AlarmKey string `json:"alarmKey"`
	// RuleID is the CONTRIBUTOR identity: the composed runtime rule id whose edge this request carries.
	// The alarm-object integrator (slice 6d-pre-2c) reference-counts contributors by it — a raised edge
	// adds/updates this rule's tier in the alarm's contributor set, a resolved edge removes it, and the
	// alarm clears when the set empties (ADR-057). Carried on every edge.
	RuleID string `json:"ruleId"`
	// Edge is AlarmEdgeRaised (rising) or AlarmEdgeResolved (falling), per ADR-057. A raised request
	// raises/escalates this rule's contribution; a resolved request removes it (clearing the alarm if
	// it was the last). The consumer routes on it (comparing the shared constant, never a literal).
	// Absent decodes as raised (the producer always sets it explicitly).
	Edge string `json:"edge,omitempty"`
	// MetricKey is the metric the rule watched, stamped onto the alarm row for context. May be
	// empty for a rule whose shape carries no single metric (e.g. a raw-CEL leaf).
	MetricKey string `json:"metricKey,omitempty"`
	// Severity is the rule's severity in the authoring vocabulary (lowercase: critical/major/…);
	// the consumer maps it to the AlarmSeverity tier (ADR-041) and drops a request whose severity
	// is unknown.
	Severity string `json:"severity"`
	// Value is the reading that drove the detection, stored as the alarm's last value. As of slice 6a
	// a value-bearing rule (threshold/repeating carry the crossing sample; deltaRate/aggregate carry
	// their computed scalar) stamps its real value here — unblocking the raise-alarm dispatch enable
	// (slice 6d). It is a POINTER so a silence-driven fire (absence/duration) or a metric-less raw-CEL
	// leaf, which has no natural value, is NULL rather than a fabricated 0 written as a real reading:
	// nil → the alarm's last_value column is left NULL. This matters when two rules share an authored
	// alarm key on one device — a value-less raise then leaves the row's last value NULL rather than
	// clobbering a co-keyed rule's real reading with 0. omitempty keeps the wire lean when absent.
	Value *float64 `json:"value,omitempty"`
	// OccurredTime is the detection's event time — the alarm's raised time AND the per-contributor
	// ordering key the alarm-object integrator (slice 6d-pre-2c) uses. ORDERING CONTRACT for the
	// contributor set: an incoming edge for a rule is applied iff its OccurredTime is >= that rule's
	// stored contributor decision time; an OLDER edge (a stale redelivery/replay) is ignored. At an
	// EQUAL OccurredTime a RESOLVE WINS a RAISE — a rising and falling edge for one rule can share an
	// event time (two events stamped identically: one matches, the next does not, ADR-057 dedup note),
	// and resolve-wins makes that zero-duration blip net to CLEARED deterministically regardless of
	// delivery order (the integrator keeps a per-contributor resolved tombstone with its ts so a
	// later-arriving equal-ts raise does not re-add). This tiebreak needs only (OccurredTime, Edge),
	// both already on the wire — no sequence field — so the contract is order-independent by
	// construction. Legacy note: the retired measurement evaluator was latest-processed-wins with no
	// equal-ts rule (api_alarm_eval.go); the integrator replaces that with this deterministic order.
	OccurredTime time.Time `json:"occurredTime"`
}

// MarshalRaiseAlarmRequest encodes a raise-alarm request (the producer side, event-processing).
func MarshalRaiseAlarmRequest(req *RaiseAlarmRequest) ([]byte, error) {
	return json.Marshal(req)
}

// UnmarshalRaiseAlarmRequest decodes a raise-alarm request (the consumer side, device-management).
func UnmarshalRaiseAlarmRequest(encoded []byte) (*RaiseAlarmRequest, error) {
	req := &RaiseAlarmRequest{}
	if err := json.Unmarshal(encoded, req); err != nil {
		return nil, err
	}
	return req, nil
}
