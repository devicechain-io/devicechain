// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import "time"

type ResetKind string

const (
	ResetReading   ResetKind = "READING"
	ResetAggregate ResetKind = "AGGREGATE"
)

// Fidelity describes what a recorded-telemetry replay can prove, including the
// structured reset comparison. Raw CEL deliberately supplies no reset evidence.
type Fidelity struct {
	Supported       bool          `json:"supported"`
	Reasons         []string      `json:"reasons"`
	Horizon         time.Duration `json:"horizon"`
	ResetKind       ResetKind     `json:"resetKind"`
	ResetStructured bool          `json:"resetStructured"`
	Metric          string        `json:"metric"`
	Op              CompareOp     `json:"op"`
	Threshold       float64       `json:"threshold"`
	GateMetric      string        `json:"gateMetric"`
	GateOp          CompareOp     `json:"gateOp"`
	GateThreshold   float64       `json:"gateThreshold"`
	WindowMode      WindowMode    `json:"windowMode"`
	Agg             AggFunc       `json:"agg"`
}

func ReplayFidelity(def Rule, compiled *CompiledRule) Fidelity {
	f := Fidelity{Supported: true}
	reject := func(s string) { f.Supported = false; f.Reasons = append(f.Reasons, s) }
	switch def.Type {
	case TypeThreshold:
		f.Horizon = 30 * time.Minute
		f.ResetKind = ResetReading
	case TypeDuration:
		f.Horizon = def.Hold.D()
		f.ResetKind = ResetReading
	case TypeAggregate:
		f.Horizon = def.Window.D()
		f.WindowMode = def.Mode
		f.ResetKind = ResetAggregate
		if def.Mode != ModeTumbling && def.Mode != ModeSliding {
			reject("the rule uses a " + string(def.Mode) + " aggregate window, which recorded-input review does not support")
		}
	default:
		reject("the rule uses " + string(def.Type) + ", which recorded-input review does not support")
	}
	if compiled == nil {
		reject("the rule has no compiled leaf")
	} else {
		if compiled.Predicate != nil && compiled.Predicate.ReferencesAttributes() {
			reject("the rule reads device attributes, which a replay of recorded telemetry cannot reproduce")
		}
		if compiled.RequiresPosition {
			reject("the rule reads geofence position, which recorded telemetry review cannot reproduce")
		}
		if compiled.KeyedByAnchor() {
			reject("the rule correlates devices by anchor, which recorded telemetry review cannot reproduce")
		}
	}
	for _, a := range def.Actions {
		switch a.Type {
		case ActionRaiseAlarm, ActionSendCommand, ActionHTTPCall, ActionPublish:
		default:
			reject("the rule uses action " + string(a.Type) + ", which recorded-input review does not support")
		}
	}
	structured := def.When.isZero() || (def.When.CEL == "" && def.When.Metric != "" && def.When.Threshold != nil && def.When.ThresholdAttr == "")
	if def.Type == TypeAggregate {
		f.ResetStructured = structured && def.Threshold != nil
		f.Metric = def.Metric
		f.Op = def.Op
		f.Agg = def.Agg
		if def.Threshold != nil {
			f.Threshold = *def.Threshold
		}
		if !def.When.isZero() && structured {
			f.GateMetric = def.When.Metric
			f.GateOp = def.When.Op
			f.GateThreshold = *def.When.Threshold
		}
	} else if !def.When.isZero() && structured {
		f.ResetStructured = true
		f.Metric = def.When.Metric
		f.Op = def.When.Op
		f.Threshold = *def.When.Threshold
	}
	return f
}
