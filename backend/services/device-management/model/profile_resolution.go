// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
)

// ProfileResolution is everything event resolution needs from a device type's active
// PUBLISHED profile version, read as ONE value: the metric definitions that validate a
// measurement and stamp its classifier and unit, and the rule-scoping identity stamped onto
// every event. Because they are one value, an event can never be validated against one
// version, stamped from a second and labelled with a third — which is what three separate
// reads could do when a publish landed between them. It is cached per (tenant, device type)
// as a single entry, read once per resolved event (ADR-016/045/051).
//
// 🔴 IT CANNOT BE CHANGED ONCE BUILT, AND THAT IS WHAT LETS THE CACHE SHARE IT. Every event
// of a device type reads the same metrics and key index out of process memory
// (messaging.GetShared), with no copy, so a write by one event would be read by every later
// one. Nothing outside this package can write to them, and the compiler is what says so:
//   - the fields are unexported, so a holder can neither set one nor reach the slice and
//     map behind them;
//   - it is handed out BY VALUE (DeviceManagementApi.ProfileResolutionByDeviceType), never
//     as a pointer to the cached one, so even assigning a whole new value (`*p = ...`, which
//     unexported fields do not prevent) lands on the holder's own copy;
//   - the accessors return values (ProfileScope holds no reference; Metric returns a
//     ResolvedMetric by value, whose own fields are unexported), never a slice, a map or a
//     pointer into it.
//
// The key index is built once, when the value is built. A publish, a rollback or a fence
// edit does not change a resolution; it evicts the entry and the next read builds a new
// one, while an event already holding the old one finishes with it whole. Code in this
// package must keep it that way: nothing here writes to a resolution after freeze returns
// it, and only this package ever holds a *ProfileResolution that the cache shares.
//
// The cache decodes it once per entry, not per event. Its encoded form is the projection
// (ResolvedMetric) rather than the full definitions, because the full ones cost several
// times as much to decode and to hold (BenchmarkDecodeProfileResolution).
type ProfileResolution struct {
	scope   ProfileScope
	metrics []ResolvedMetric
	// byKey indexes metrics by key, pointing into metrics. Built once, by freeze; nil
	// when no metric is declared.
	byKey map[string]*ResolvedMetric
}

// NewProfileResolution builds a resolution from a scope and the definitions of the
// version the scope names, projecting each one. nil definitions declare no metrics.
func NewProfileResolution(scope ProfileScope, defs []*MetricDefinition) ProfileResolution {
	metrics := make([]ResolvedMetric, 0, len(defs))
	for _, def := range defs {
		metrics = append(metrics, def.Resolved())
	}
	return *freeze(scope, metrics)
}

// freeze builds the resolution around metrics, which it takes ownership of: nothing else
// may hold the slice afterwards.
func freeze(scope ProfileScope, metrics []ResolvedMetric) *ProfileResolution {
	r := &ProfileResolution{scope: scope, metrics: metrics}
	if len(metrics) > 0 {
		r.byKey = make(map[string]*ResolvedMetric, len(metrics))
		// A key declared twice resolves to its last declaration.
		for i := range metrics {
			r.byKey[metrics[i].m.MetricKey] = &metrics[i]
		}
	}
	return r
}

// The accessors have value receivers: the zero ProfileResolution is a valid one that
// declares nothing, and a resolution returned by a call can be read without a variable.

// Scope is the rule-scoping identity and fence-set version stamped onto the event. It is
// a copy, and shares nothing: ProfileScope holds no slice, map or pointer
// (TestAResolutionHandsOutNothingWritable).
func (r ProfileResolution) Scope() ProfileScope {
	return r.scope
}

// Metric is the declared metric with key, if there is one, as a copy.
func (r ProfileResolution) Metric(key string) (ResolvedMetric, bool) {
	if m, ok := r.byKey[key]; ok {
		return *m, true
	}
	return ResolvedMetric{}, false
}

// MetricCount is how many metrics the version declares: 0 for an untyped device type or
// an unpublished profile.
func (r ProfileResolution) MetricCount() int {
	return len(r.metrics)
}

// ValidateMeasurement looks up the definition for a measurement by its key and validates
// the value against it. Lenient by design (ADR-016): a measurement whose key is not
// declared on the profile passes through unvalidated, so the metric model is an additive
// typing layer rather than a strict allow-list.
func (r ProfileResolution) ValidateMeasurement(name string, value string) error {
	def, declared := r.Metric(name)
	if !declared {
		return nil
	}
	return ValidateMetricValue(&def, value)
}

// profileResolutionWire is a resolution's encoded form, the one the cache stores. Its
// field names are the ones ProfileResolution encoded as when its fields were exported, so
// an entry written by either reads the same (TestProfileResolutionWireIsUnchanged).
type profileResolutionWire struct {
	Scope   ProfileScope
	Metrics []resolvedMetricWire
}

// MarshalJSON encodes the resolution as the cache stores it. A value receiver, so a
// ProfileResolution encodes the same whether or not it is behind a pointer.
func (r ProfileResolution) MarshalJSON() ([]byte, error) {
	w := profileResolutionWire{Scope: r.scope}
	if r.metrics != nil {
		w.Metrics = make([]resolvedMetricWire, len(r.metrics))
		for i := range r.metrics {
			w.Metrics[i] = r.metrics[i].m
		}
	}
	return json.Marshal(w)
}

// errDecodeInPlace is what decoding into an existing resolution answers.
var errDecodeInPlace = errors.New("model: a ProfileResolution is never decoded in place, " +
	"which would change one that may be shared; decode a new one (decodeProfileResolution)")

// UnmarshalJSON refuses, and writes nothing: a resolution is never changed once built. It
// exists because, without it, json.Unmarshal into a ProfileResolution would SUCCEED and
// leave it empty (its fields are unexported): a resolution declaring no metrics and no
// scope, which nothing downstream can tell from a real one.
func (r *ProfileResolution) UnmarshalJSON([]byte) error {
	return errDecodeInPlace
}

// decodeProfileResolution builds a resolution from its encoded form. It is the cache's
// decode (messaging.GetShared), run once per cached entry, and so where the key index
// is built.
func decodeProfileResolution(raw []byte) (*ProfileResolution, error) {
	var w profileResolutionWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	var metrics []ResolvedMetric
	if w.Metrics != nil {
		metrics = make([]ResolvedMetric, len(w.Metrics))
		for i := range w.Metrics {
			metrics[i] = ResolvedMetric{m: w.Metrics[i]}
		}
	}
	return freeze(w.Scope, metrics), nil
}
