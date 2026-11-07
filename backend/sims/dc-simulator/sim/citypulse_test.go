// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/outcome"
)

func TestCitypulseIsRegisteredAndShaped(t *testing.T) {
	s, err := NewSim("citypulse", 7, Load{})
	if err != nil {
		t.Fatal(err)
	}
	m := s.Manifest()
	if len(m.Areas) != citypulseDistrictCount || m.Populations[0].Count != citypulseNodeCount {
		t.Fatalf("areas %d, nodes %d", len(m.Areas), m.Populations[0].Count)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	devices := m.Expand(m.Seed)
	if len(devices) != citypulseNodeCount {
		t.Fatalf("devices = %d", len(devices))
	}
	// The tick's device-to-district rule must be Expand's own, or a district's readings would
	// land on another district's anchor.
	for i, d := range devices {
		var got string
		for _, a := range d.Assignments {
			if a.TargetType == "area" {
				got = a.TargetToken
			}
		}
		if want := CitypulseDistrictToken(i%citypulseDistrictCount + 1); got != want {
			t.Fatalf("device %d is in %q, the tick treats it as in %q", i, got, want)
		}
	}
}

func TestCitypulseReadingsAreDeterministicAndInRange(t *testing.T) {
	for d := 0; d < citypulseDistrictCount; d++ {
		for node := 0; node < citypulseNodesPerDist; node++ {
			for tick := int64(1_800_000_000); tick < 1_800_000_000+6*citypulseBlockSeconds*5; tick += 7 {
				a := citypulseReading(d, node, tick)
				b := citypulseReading(d, node, tick)
				if a[CitypulseLampKey] != b[CitypulseLampKey] || a[CitypulsePM25Key] != b[CitypulsePM25Key] {
					t.Fatal("not deterministic")
				}
				if l := a[CitypulseLampKey]; l != 0 && l != 1 {
					t.Fatalf("lamp = %v", l)
				}
				if v := a[CitypulsePM25Key]; v <= 0 || v > 60 {
					t.Fatalf("pm25 = %v", v)
				}
				if v := a[CitypulseParkingKey]; v < 0 || v > 100 {
					t.Fatalf("parking = %v", v)
				}
			}
		}
	}
}

// The scenario exists to make one point: the city-wide figure can pass while a district fails.
// This holds the demo to it by judging the readings it emits with the outcome evaluator, the
// way the seeded outcomes do (five minute buckets over the last hour).
func TestCitypulseDemoHasAFailingDistrictBehindAPassingCity(t *testing.T) {
	now := time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)
	const buckets = 12
	// One bucket is one wall-clock-aligned block, so a bad block is exactly one bad bucket.
	areas := make([]string, citypulseDistrictCount)
	for i := range areas {
		areas[i] = CitypulseDistrictToken(i + 1)
	}
	judge := func(metric string, cmp outcome.Comparator, threshold, objective float64) outcome.Result {
		def := outcome.Definition{
			Token: metric, Metric: metric, Aggregation: outcome.Avg, Comparator: cmp, Threshold: threshold,
			Areas: areas, Window: time.Hour, Interval: 5 * time.Minute, Objective: objective,
		}
		span, err := outcome.SpanFor(def, now)
		if err != nil {
			t.Fatal(err)
		}
		inputs := make([]outcome.AreaInput, citypulseDistrictCount)
		for d := range inputs {
			inputs[d].Area = areas[d]
			for b := int64(0); b < buckets; b++ {
				var sum float64
				var n int64
				start := span.First.Add(time.Duration(b) * span.Interval).Unix()
				for node := 0; node < citypulseNodesPerDist; node++ {
					// A reading every five seconds across the bucket, whatever the run's own start.
					for k := int64(0); k < citypulseBlockSeconds; k += 5 {
						sum += citypulseReading(d, node, start+k)[metric]
						n++
					}
				}
				avg := sum / float64(n)
				inputs[d].Buckets = append(inputs[d].Buckets, outcome.Bucket{
					Start: span.First.Add(time.Duration(b) * span.Interval), Count: n, Avg: &avg,
				})
			}
		}
		res, err := outcome.Evaluate(def, now, inputs)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Lighting: Eastside loses a lamp-bank one block in three. Over the 12 blocks of the hour
	// (starting at a block that is a multiple of 60 so the pattern is aligned) that is 4 bad.
	lighting := judge(CitypulseLampKey, outcome.GreaterEqual, 0.9, 0.9)
	if lighting.Areas[3].Status != outcome.StatusBreached {
		t.Fatalf("district 4 lighting = %+v", lighting.Areas[3])
	}
	for _, a := range lighting.Areas[:3] {
		if a.Status != outcome.StatusOK {
			t.Fatalf("%s lighting = %+v", a.Area, a)
		}
	}
	if lighting.Equity.Pooled < 0.9 {
		t.Fatalf("the city-wide figure must pass for the demo to make its point: %v", lighting.Equity.Pooled)
	}
	if lighting.Equity.WorstArea != CitypulseDistrictToken(4) {
		t.Fatalf("worst = %s", lighting.Equity.WorstArea)
	}

	// Air quality: only district 3 has an episode.
	air := judge(CitypulsePM25Key, outcome.LessEqual, 15, 0.8)
	if air.Areas[2].Status == outcome.StatusOK || air.Areas[0].Status != outcome.StatusOK {
		t.Fatalf("air = %+v", air.Areas)
	}
}
