// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// benchRules returns n realistic rules for one profile, cycling through the shapes the authoring
// surfaces produce: a structured threshold compare, a raw-CEL leaf that reads a map key behind a
// presence guard (the has()/`in` idiom), and a raw-CEL leaf comparing the sample's time.
func benchRules(b *testing.B, n int) []ScopedRule {
	b.Helper()
	out := make([]ScopedRule, 0, n)
	for i := 0; i < n; i++ {
		id := ComposeRuleID("acme", fmt.Sprintf("r%d", i))
		var r rules.Rule
		switch i % 3 {
		case 0:
			r = rules.Rule{ID: id, Name: "over", Type: rules.TypeThreshold,
				When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)}}
		case 1:
			r = rules.Rule{ID: id, Name: "raw map", Type: rules.TypeThreshold,
				When: rules.Condition{CEL: `"humidity" in m && m["humidity"] > 60.0 && !("limit" in attr)`}}
		default:
			r = rules.Rule{ID: id, Name: "raw time", Type: rules.TypeDuration,
				When: rules.Condition{CEL: `"temperature" in m && occurred > timestamp("2020-01-01T00:00:00Z") && m["temperature"] > 70.0`},
				Hold: rules.Duration(time.Minute)}
		}
		cr, err := rules.Compile(r, rules.Limits{})
		if err != nil {
			b.Fatalf("compile %q: %v", id, err)
		}
		out = append(out, ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr})
	}
	return out
}

// BenchmarkPredicateEval measures the cost of evaluating every rule of a profile for ONE sample
// through the entry point the detection core uses (RuleRegistry.Plan -> CompiledRule.BuildEvent ->
// Predicate.Eval). ns/op and allocs/op are therefore per sample, at 1, 10 and 50 rules.
func BenchmarkPredicateEval(b *testing.B) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			reg := NewRuleRegistry(benchRules(b, n))
			ev := measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90", "humidity": "70"})
			attr := map[string]float64{"other": 1}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res := reg.Plan(uint64(i), "acme", ev, base, attr, nil)
				if len(res.Events) == 0 || res.EvalErrors != 0 {
					b.Fatalf("unexpected plan: %d events, %d errors", len(res.Events), res.EvalErrors)
				}
			}
		})
	}
}

// batchEvent builds a resolved measurement event carrying `samples` batched samples (a
// store-and-forward upload), each reporting temperature and humidity.
func batchEvent(device, profileVersion string, base time.Time, samples int) *resolvedevent.ResolvedEvent {
	entries := make([]resolvedevent.ResolvedMeasurementsEntry, 0, samples)
	for i := 0; i < samples; i++ {
		entries = append(entries, resolvedevent.ResolvedMeasurementsEntry{
			OccurredTime: base.Add(time.Duration(i) * time.Second),
			Entries: []resolvedevent.ResolvedMeasurementEntry{
				{Name: "temperature", Value: "90"}, {Name: "humidity", Value: "70"},
			},
		})
	}
	return &resolvedevent.ResolvedEvent{
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        base,
		Payload:             &resolvedevent.ResolvedMeasurementsPayload{Entries: entries},
	}
}

// BenchmarkPlanShapes measures one Plan call across rules per profile x samples per event, the two
// axes the per-event planning cost scales with. ns/op and allocs/op are per EVENT.
func BenchmarkPlanShapes(b *testing.B) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	attr := map[string]float64{"other": 1}
	for _, n := range []int{1, 10, 50} {
		for _, k := range []int{1, 8, 32} {
			b.Run(fmt.Sprintf("rules=%d/samples=%d", n, k), func(b *testing.B) {
				reg := NewRuleRegistry(benchRules(b, n))
				ev := batchEvent("d1", "p@1", base, k)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					res := reg.Plan(uint64(i), "acme", ev, base, attr, nil)
					if len(res.Events) == 0 || res.EvalErrors != 0 {
						b.Fatalf("unexpected plan: %d events, %d errors", len(res.Events), res.EvalErrors)
					}
				}
			})
		}
	}
}
