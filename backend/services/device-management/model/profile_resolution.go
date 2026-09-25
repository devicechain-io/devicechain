// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// ProfileResolution is everything event resolution needs from a device type's active
// PUBLISHED profile version, read as ONE value: the metric definitions that validate a
// measurement and stamp its classifier and unit, and the rule-scoping identity stamped onto
// every event. Because they are one value, an event can never be validated against one
// version, stamped from a second and labelled with a third — which is what three separate
// reads could do when a publish landed between them. It is cached per (tenant, device type)
// as a single entry, read once per resolved event (ADR-016/045/051).
//
// 🔴 EVERY EVENT DECODES THE WHOLE ENTRY, METRICS INCLUDED, and that is measured rather
// than assumed (BenchmarkDecodeProfileResolution). The metrics are projected to what
// resolution reads (ResolvedMetric) because the full definitions cost several times as
// much to decode. Leaving them undecoded for the event types that do not read them was
// tried and measured too, and it does not pay: encoding/json scans a raw message about as
// slowly as it decodes one, so location events saved little while every measurement paid
// for two passes. Splitting the metrics back out would save the location path the decode
// at the price of a second read per measurement and two versions per event again.
type ProfileResolution struct {
	// Scope is the rule-scoping identity and fence-set version stamped onto the event.
	Scope ProfileScope
	// Metrics are the published version's metric definitions, projected to what
	// resolution reads (see ResolvedMetric). Empty for an untyped device type or an
	// unpublished profile.
	Metrics []ResolvedMetric
}

// NewProfileResolution builds a resolution from a scope and the definitions of the
// version the scope names, projecting each one. nil definitions declare no metrics.
func NewProfileResolution(scope ProfileScope, defs []*MetricDefinition) *ProfileResolution {
	metrics := make([]ResolvedMetric, 0, len(defs))
	for _, def := range defs {
		metrics = append(metrics, def.Resolved())
	}
	return &ProfileResolution{Scope: scope, Metrics: metrics}
}

// MetricsByKey indexes Metrics by metric key. The map is empty, not nil, when none are
// declared, including for a nil resolution.
func (r *ProfileResolution) MetricsByKey() map[string]*ResolvedMetric {
	if r == nil {
		return map[string]*ResolvedMetric{}
	}
	byKey := make(map[string]*ResolvedMetric, len(r.Metrics))
	for i := range r.Metrics {
		byKey[r.Metrics[i].MetricKey] = &r.Metrics[i]
	}
	return byKey
}
