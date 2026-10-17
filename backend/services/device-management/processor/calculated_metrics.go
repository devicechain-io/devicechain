// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Calculated metrics at resolve (ADR-082): what the resolver reports about them.
//
// Every outcome is counted, label-bounded: calcmetric_evaluations_total for a value
// computed, calcmetric_skipped_total{reason} for one that was not (the closed set
// calcfield.SkipReasons), and calcmetric_device_key_dropped_total for a value a device sent
// under a calculated key, which the resolver drops because the computed value is
// authoritative. No metric key, tenant or device is a label: those are unbounded. They are
// in the log line instead, which is rate-limited to one per (tenant, profile version,
// metric, outcome) per process, so a formula that skips on every reading of a fleet reports
// once rather than once a reading.

// calcWarnCapacity bounds the rate limiter's memory of what it has already reported.
const calcWarnCapacity = 4096

// CalcMetrics is the resolver pool's calculated-metric instruments, shared by every worker.
// The zero value counts nothing and logs nothing (a resolver built in a test).
type CalcMetrics struct {
	evaluations      prometheus.Counter
	deviceKeyDropped prometheus.Counter
	// skipped holds one child of calcmetric_skipped_total per reason, resolved once, so a
	// skip costs no label lookup.
	skipped map[calcfield.SkipReason]prometheus.Counter
	warned  *lruSet[struct{}]
}

// newCalcMetrics builds the instruments, every reason's series exported at zero. Called
// from NewResolveMetrics, so once per process.
func newCalcMetrics(ms *core.Microservice) CalcMetrics {
	vec := ms.NewCounterVec("calcmetric_skipped_total",
		"Calculated-metric values not produced for a sample, by reason (missing_input, invalid_input, non_finite, div_zero, overflow, range, unstorable, eval_error); the sample's other values are stored",
		[]string{"reason"})
	skipped := make(map[calcfield.SkipReason]prometheus.Counter, len(calcfield.SkipReasons))
	for _, reason := range calcfield.SkipReasons {
		skipped[reason] = vec.WithLabelValues(string(reason))
	}
	return CalcMetrics{
		evaluations: ms.NewCounter("calcmetric_evaluations_total",
			"Calculated-metric values computed at resolve and appended to their reading"),
		deviceKeyDropped: ms.NewCounter("calcmetric_device_key_dropped_total",
			"Values a device sent under a calculated metric's key, dropped in favour of the computed one"),
		skipped: skipped,
		warned:  newLruSet[struct{}](calcWarnCapacity),
	}
}

// calcObserver reports one event's calculated-metric outcomes. A resolver holds one as a
// field and points it at the event being resolved, so reporting through the
// model.CalculationObserver interface allocates nothing; a resolver resolves one event at a
// time, which is what makes the field safe.
type calcObserver struct {
	metrics *CalcMetrics
	ctx     context.Context
	device  string
	version string
	// seen is this observer's own memory of what it has already put to the shared set. A
	// resolver is single-threaded, so it needs no lock, and a repeat skip (the common case:
	// a has()-guarded input that is usually absent skips on nearly every reading) is answered
	// here without building a key string or touching the pool-wide mutex. It is a cache in
	// front of the shared set, never a second authority: a miss still asks the shared set,
	// which alone decides whether this process reports. Cleared when full, so it is bounded.
	//
	// Each entry holds the shared set's insert count when this observer last touched the key
	// there. The shared set is an LRU, and the old path's touch on every repeat is what kept
	// a hot outcome from ageing out of it; a cache that never touched it again would let a hot
	// outcome be evicted under churn and reported a second time (by any observer, or by this
	// one after a clear). So a hit older than half the shared capacity in inserts falls
	// through and touches the shared set once more, which keeps it held while it stays hot.
	seen map[claimKey]uint64
}

// claimKey is the identity of one reportable outcome: (tenant, profile version, metric,
// outcome). A struct of strings, so building it allocates nothing.
type claimKey struct {
	tenant, version, key, outcome string
}

// Computed implements model.CalculationObserver.
func (o *calcObserver) Computed(string) {
	if o.metrics != nil && o.metrics.evaluations != nil {
		o.metrics.evaluations.Inc()
	}
}

// Skipped implements model.CalculationObserver.
func (o *calcObserver) Skipped(key string, reason calcfield.SkipReason, err error) {
	if o.metrics == nil {
		return
	}
	if c := o.metrics.skipped[reason]; c != nil {
		c.Inc()
	}
	if o.claim(key, string(reason)) {
		ev := log.Warn().Str("device", o.device).Str("profileVersion", o.version).Str("metric", key).
			Str("reason", string(reason))
		if err != nil {
			ev = ev.Err(err)
		}
		ev.Msg("Calculated metric produced no value for a reading (reported once per tenant, profile version, metric and reason)")
	}
}

// deviceKeyDropped counts, and reports once, a device-sent value under a calculated key.
func (o *calcObserver) deviceKeyDropped(key, value string) {
	if o.metrics == nil {
		return
	}
	if o.metrics.deviceKeyDropped != nil {
		o.metrics.deviceKeyDropped.Inc()
	}
	if o.claim(key, "device_key") {
		dropMeasurement(o.device, key, value, "calculated key sent by device (reported once per tenant, "+
			"profile version and metric; the computed value is stored instead)")
	}
}

// claim reports whether this (tenant, profile version, metric, outcome) is being reported
// for the first time in this process.
//
// The per-observer cache answers a repeat without a lock. A key it has not seen goes to the
// shared set, and is remembered here whatever the answer: if another worker already reported
// it, this one has nothing to report either. So the log stays bounded by the shared set exactly
// as before; only the cost of repeating a suppressed outcome moved.
func (o *calcObserver) claim(key, outcome string) bool {
	if o.metrics.warned == nil {
		return false
	}
	tenant, _ := core.TenantFromContext(o.ctx)
	k := claimKey{tenant: tenant, version: o.version, key: key, outcome: outcome}
	now := o.metrics.warned.insertCount()
	if at, ok := o.seen[k]; ok && now-at < calcWarnRefresh {
		return false
	}
	if o.seen == nil || len(o.seen) >= calcWarnCapacity {
		o.seen = make(map[claimKey]uint64, 64)
	}
	o.seen[k] = now
	return o.metrics.warned.putIfAbsent(claimString(k), struct{}{})
}

// calcWarnRefresh is how many shared inserts a cached claim answers for before it touches
// the shared set again: half its capacity, so a key used within that window cannot have
// been evicted, and is moved back to the front well before it could be.
const calcWarnRefresh = calcWarnCapacity / 2

// claimString is the shared set's key for a claim.
func claimString(k claimKey) string {
	return k.tenant + "|" + k.version + "|" + k.key + "|" + k.outcome
}

// The observer is what the resolution reports to.
var _ model.CalculationObserver = (*calcObserver)(nil)
