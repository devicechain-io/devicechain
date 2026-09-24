// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMeteringTimeChoosesStampThenAppendThenNow(t *testing.T) {
	appended := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name              string
		stamped, appendAt time.Time
		want              time.Time
		clock             MeteringClock
	}{
		{"stamp before append", appended.Add(-time.Minute), appended, appended.Add(-time.Minute), ClockStamped},
		{"stamp equal to append", appended, appended, appended, ClockStamped},
		{"stamp with no append", appended, time.Time{}, appended, ClockStamped},
		{"stamp after append is capped", appended.Add(time.Hour), appended, appended, ClockAppend},
		{"no stamp", time.Time{}, appended, appended, ClockAppend},
		{"neither", time.Time{}, time.Time{}, time.Time{}, ClockNow},
	}
	for _, c := range cases {
		got, clk := MeteringTime(c.stamped, c.appendAt)
		if !got.Equal(c.want) || clk != c.clock {
			t.Errorf("%s: MeteringTime = %v/%s, want %v/%s", c.name, got, clk, c.want, c.clock)
		}
	}
}

// Both fallback children exist at 0 before anything falls back, a stamped result counts
// nothing, and each fallback counts under its own source.
func TestRateClockFallbacksCountEveryFallbackAndOnlyThose(t *testing.T) {
	ms := &Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	record := NewRateClockFallbacks(ms)

	read := func() map[string]float64 {
		vals := map[string]float64{}
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "devicechain_eventprocessing_rate_clock_fallback_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				vals[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
			}
		}
		return vals
	}
	if got := read(); len(got) != 2 || got["append"] != 0 || got["now"] != 0 {
		t.Fatalf("series before any fallback = %v, want append and now at 0", got)
	}
	record(ClockStamped)
	record(ClockAppend)
	record(ClockNow)
	record(ClockNow)
	if got := read(); len(got) != 2 || got["append"] != 1 || got["now"] != 2 {
		t.Fatalf("fallback counts = %v, want append=1 now=2 and no stamped series", got)
	}
}
