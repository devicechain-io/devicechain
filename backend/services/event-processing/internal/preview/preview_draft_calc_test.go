// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"sync"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"gorm.io/gorm"
)

// Draft calculated metrics in the preview (ADR-082, preview parity). History was resolved under
// the published version, whose `heat` formula was `temp`; the draft changes it to `temp * 2.0`.
// The rule under preview is `heat > 100`.

// storedMsg is a resolved measurement message as the published version stored it: the raw
// temp reading and its calculated heat (= temp), flagged Derived.
func storedMsg(t *testing.T, seq uint64, tenant, temp, heat string, occurred time.Time) messaging.Message {
	t.Helper()
	entries := []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: temp}}
	if heat != "" {
		entries = append([]resolvedevent.ResolvedMeasurementEntry{{Name: "heat", Value: heat, Derived: true}}, entries...)
	}
	ev := &resolvedevent.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: "d1", ProfileVersionToken: "p@1",
		OccurredTime: occurred, ProcessedTime: occurred, EventType: esmodel.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: occurred, Entries: entries,
		}}},
	}
	b, err := resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

func heatReg(t *testing.T, tenant string) *runtime.RuleRegistry {
	t.Helper()
	thr := 100.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: tenant + "/p@1/heat", Name: "heat", Type: rules0.TypeThreshold, Severity: rules0.SeverityMajor,
		When: rules0.Condition{Metric: "heat", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: tenant, ProfileVersionToken: "p@1", Compiled: cr}})
}

// draftWith is a draft resolution declaring temp measured and, when expr is not empty, heat
// calculated by expr.
func draftWith(expr string) *dmmodel.ProfileResolution {
	defs := []*dmmodel.MetricDefinition{{Model: gorm.Model{ID: 1}, MetricKey: "temp", DataType: "DOUBLE"}}
	if expr != "" {
		defs = append(defs, &dmmodel.MetricDefinition{Model: gorm.Model{ID: 2}, MetricKey: "heat", DataType: "DOUBLE",
			Expression: sql.NullString{String: expr, Valid: true}})
	}
	if problems := dmmodel.CheckDraftCalculatedMetrics(defs); len(problems) > 0 {
		panic(fmt.Sprint(problems))
	}
	r := dmmodel.NewProfileResolution(dmmodel.ProfileScope{}, defs)
	return &r
}

// history: temp 60 (stored heat 60), then temp 40 (stored heat 40).
func heatHistory(t *testing.T) []messaging.Message {
	return []messaging.Message{
		storedMsg(t, 1, "acme", "60", "60", base),
		storedMsg(t, 2, "acme", "40", "40", base.Add(time.Minute)),
	}
}

func raises(res Result) int {
	n := 0
	for _, f := range res.Firings {
		if f.Raise {
			n++
		}
	}
	return n
}

// The stored values do not fire the rule; the draft's do, at the reading whose draft value is
// above the threshold, carrying the draft's value.
func TestADraftFormulaIsPreviewedFromTheStoredInputs(t *testing.T) {
	stored, err := Run(context.Background(), &fakeOpener{msgs: heatHistory(t)}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raises(stored) != 0 || stored.Stats.RecalculatedSamples != 0 {
		t.Fatalf("the stored values (heat 60, 40) must not fire heat > 100: %+v", stored)
	}

	drafted, err := Run(context.Background(), &fakeOpener{msgs: heatHistory(t)}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, draftWith("temp * 2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if raises(drafted) != 1 || !drafted.Firings[0].OccurredAt.Equal(base) || drafted.Firings[0].Value != 120 {
		t.Fatalf("the draft's heat (120 at base, then 80) must raise once at base with 120: %+v", drafted.Firings)
	}
	if drafted.Stats.RecalculatedSamples != 2 || drafted.Stats.CalculatedSkips != 0 {
		t.Fatalf("stats = %+v", drafted.Stats)
	}
}

// A draft that deletes the formula previews no calculated value at all — the stored one is not
// kept — and one whose formula cannot produce a value counts the skip.
func TestADraftWithoutTheFormulaPreviewsNoValue(t *testing.T) {
	hot := []messaging.Message{storedMsg(t, 1, "acme", "150", "150", base)}
	asStored, err := Run(context.Background(), &fakeOpener{msgs: hot}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, nil)
	if err != nil || raises(asStored) != 1 {
		t.Fatalf("the stored heat 150 must fire: %+v %v", asStored, err)
	}
	deleted, err := Run(context.Background(), &fakeOpener{msgs: hot}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, draftWith(""))
	if err != nil || raises(deleted) != 0 {
		t.Fatalf("a draft without heat must not fire on the stored heat: %+v %v", deleted, err)
	}
	if deleted.Stats.RecalculatedSamples != 1 {
		t.Fatalf("removing the stored value is a recalculation: %+v", deleted.Stats)
	}
	// A formula-free draft over history with no calculated value leaves every event as it is.
	plain := []messaging.Message{storedMsg(t, 1, "acme", "150", "", base)}
	untouched, err := Run(context.Background(), &fakeOpener{msgs: plain}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, draftWith(""))
	if err != nil || untouched.Stats.RecalculatedSamples != 0 || untouched.Stats.EventsScanned != 1 {
		t.Fatalf("a formula-free draft recalculated an event with nothing to recalculate: %+v %v", untouched, err)
	}
	skipping, err := Run(context.Background(), &fakeOpener{msgs: hot}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, draftWith("temp / 0.0"))
	if err != nil || raises(skipping) != 0 || skipping.Stats.CalculatedSkips != 1 {
		t.Fatalf("a non-finite draft value must be skipped and counted, never fire: %+v %v", skipping, err)
	}
}

// TestDraftCalculationLeaksNowhere: what a draft computes stays in the preview that computed it.
//   - The replayed messages are byte-identical afterwards: nothing is written back to what the
//     stream delivered.
//   - Another tenant's events on the same stream, under the same profile-version token, are
//     neither scanned nor recalculated: the draft is applied only after the tenant filter.
//   - Nothing survives the call: a preview without the draft over the same history, run
//     afterwards, sees the stored values; and previews of different drafts running at once each
//     see only their own (run with -race to also prove they share no memory).
//
// Run takes no writer, store or live engine at all, so a draft value has no path to storage or
// to live detection; this test covers the paths it does have.
func TestDraftCalculationLeaksNowhere(t *testing.T) {
	msgs := append(heatHistory(t),
		storedMsg(t, 3, "globex", "70", "70", base.Add(2*time.Minute)),
		storedMsg(t, 4, "globex", "90", "90", base.Add(3*time.Minute)))
	before := make([][]byte, len(msgs))
	for i, m := range msgs {
		before[i] = bytes.Clone(m.Value)
	}

	res, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, draftWith("temp * 2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.EventsScanned != 2 || res.Stats.RecalculatedSamples != 2 {
		t.Fatalf("globex's events reached the draft: %+v", res.Stats)
	}
	for i, m := range msgs {
		if !bytes.Equal(before[i], m.Value) {
			t.Fatalf("replayed message %d was written", i)
		}
	}
	// globex's own preview of the same version, with no draft, sees its stored values only.
	globex, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", heatReg(t, "globex"),
		"globex", "p@1", window(), 0, 0, 0, nil, nil)
	if err != nil || raises(globex) != 0 || globex.Stats.EventsScanned != 2 {
		t.Fatalf("globex (stored heat 70, 90) must not fire: %+v %v", globex, err)
	}

	after, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, nil)
	if err != nil || raises(after) != 0 {
		t.Fatalf("a draft value outlived its preview: %+v %v", after, err)
	}

	// Concurrent previews of different drafts: ×2 fires at 60 (120), ×1.5 fires nowhere (90, 60),
	// ×3 fires at 60 (180) and at 40 (120) without resolving in between.
	cases := []struct {
		expr  string
		value float64
	}{{"temp * 2.0", 120}, {"temp * 1.5", 0}, {"temp * 3.0", 180}}
	var wg sync.WaitGroup
	errs := make([]error, len(cases)*20)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := cases[i%len(cases)]
			r, err := Run(context.Background(), &fakeOpener{msgs: heatHistory(t)}, "resolved-events", heatReg(t, "acme"),
				"acme", "p@1", window(), 0, 0, 0, nil, draftWith(c.expr))
			switch {
			case err != nil:
				errs[i] = err
			case c.value == 0 && raises(r) != 0:
				errs[i] = fmt.Errorf("%s fired: %+v", c.expr, r.Firings)
			case c.value != 0 && (raises(r) != 1 || r.Firings[0].Value != c.value):
				errs[i] = fmt.Errorf("%s: want one raise at %v, got %+v", c.expr, c.value, r.Firings)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}
