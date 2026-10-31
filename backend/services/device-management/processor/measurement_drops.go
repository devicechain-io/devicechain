// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
)

// Why a measurement entry was dropped at resolution while the rest of its event was kept. A
// closed set, because it is a metric label.
const (
	// dropUndeclaredNonNumeric: the key is not on the profile and its value is not a number
	// (a text value such as "ok"), so it cannot go in the numeric measurement column.
	dropUndeclaredNonNumeric = "undeclared_non_numeric"
	// dropUndeclaredUnstorable: the key is not on the profile and its value is numeric but
	// cannot be stored (NaN, +/-Inf, or beyond decimal(20,8)).
	dropUndeclaredUnstorable = "undeclared_unstorable"
	// dropDeclaredNotStorable: the key is declared with a data type that is not a time-series
	// metric (defensive: creating such a definition is already refused).
	dropDeclaredNotStorable = "declared_not_storable"
)

var measurementDropReasons = []string{dropUndeclaredNonNumeric, dropUndeclaredUnstorable, dropDeclaredNotStorable}

// MeasurementDropMetrics counts measurement entries discarded at resolution while the event's
// other entries were stored. The acknowledgement a device gets does not say so and the event is
// not dead-lettered, so this counter and the rate-limited warning are how an operator learns a
// device's payload is losing data. No tenant, device or metric key is a label (unbounded); the
// warning carries them, once per (tenant, device, metric, reason) per process.
//
// The zero value counts nothing and logs nothing (a resolver built in a test).
type MeasurementDropMetrics struct {
	dropped map[string]prometheus.Counter
	warned  *lruSet[struct{}]
}

// newMeasurementDropMetrics builds the instruments, every reason's series exported at zero.
// Called from NewResolveMetrics, so once per process.
func newMeasurementDropMetrics(ms *core.Microservice) *MeasurementDropMetrics {
	vec := ms.NewCounterVec("resolve_measurement_dropped_total",
		"Measurement entries dropped at resolution while the rest of the event was stored, by reason "+
			"(undeclared_non_numeric: a text value for a key the profile does not declare; undeclared_unstorable: "+
			"NaN, infinite or out-of-range numeric value for an undeclared key; declared_not_storable: a declared "+
			"key whose data type is not a time-series metric). The device's acknowledgement does not report the drop.",
		[]string{"reason"})
	dropped := make(map[string]prometheus.Counter, len(measurementDropReasons))
	for _, r := range measurementDropReasons {
		dropped[r] = vec.WithLabelValues(r)
	}
	return &MeasurementDropMetrics{dropped: dropped, warned: newLruSet[struct{}](calcWarnCapacity)}
}

// record counts one dropped entry and warns the first time this (tenant, device, metric,
// reason) is seen in this process. A nil receiver records nothing.
func (m *MeasurementDropMetrics) record(tenant, device, key, value, reason, detail string) {
	if m == nil {
		return
	}
	if c := m.dropped[reason]; c != nil {
		c.Inc()
	}
	if m.warned == nil || !m.warned.putIfAbsent(tenant+"\x00"+device+"\x00"+key+"\x00"+reason, struct{}{}) {
		return
	}
	log.Warn().Str("tenant", tenant).Str("device", device).Str("metric", key).Str("value", value).
		Str("reason", reason).Str("detail", detail).
		Msg("Dropping unstorable measurement; the rest of the event was stored " +
			"(declare it as a numeric metric to persist; reported once per tenant, device, metric and reason)")
}
