// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

type benchObserver struct{ computed, skipped int }

func (o *benchObserver) Computed(string)                             { o.computed++ }
func (o *benchObserver) Skipped(string, calcfield.SkipReason, error) { o.skipped++ }

// BenchmarkAppend is the per-sample-entry cost of Calculation.Append over four calculated
// metrics, for a sample that carries every input ("full": the common single-message device)
// and for one that carries only one input of a multi-message device ("partial": most formulas
// are not applicable to it).
func BenchmarkAppend(b *testing.B) {
	def := func(id uint, key, expr string) *MetricDefinition {
		m := &MetricDefinition{MetricKey: key, DataType: "DOUBLE"}
		m.ID = id
		if expr != "" {
			m.Expression = sql.NullString{String: expr, Valid: true}
		}
		return m
	}
	r := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@1"}, []*MetricDefinition{
		def(1, "voltage", ""), def(2, "current", ""), def(3, "temp", ""), def(4, "battery", ""),
		def(10, "power", "voltage * current"), def(11, "temp_f", "temp * 1.8 + 32.0"),
		def(12, "battery_pct", "battery * 100.0"), def(13, "power_x2", "power * 2.0"),
	})
	run := func(b *testing.B, in map[string]string) {
		ms := map[string]ResolvedMetric{}
		for k := range in {
			m, _ := r.Metric(k)
			ms[k] = m
		}
		obs := &benchObserver{}
		dst := make([]resolvedevent.ResolvedMeasurementEntry, 0, 8)
		b.ReportAllocs()
		for b.Loop() {
			calc := r.BeginCalculation(nil)
			for k, v := range in {
				m := ms[k]
				calc.Set(&m, v)
			}
			dst = calc.Append(dst[:0], obs)
			calc.Release()
		}
	}
	b.Run("full", func(b *testing.B) {
		run(b, map[string]string{"voltage": "230.5", "current": "12.25", "temp": "21.5", "battery": "0.8"})
	})
	b.Run("partial", func(b *testing.B) { run(b, map[string]string{"temp": "21.5"}) })
}
