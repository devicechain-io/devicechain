// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"
)

// observableBatchMetrics builds a BatchMetrics against vectors this test owns.
//
// 🔴 IT DOES NOT USE NewBatchMetrics, AND IT MUST NOT. That constructor goes through
// promauto, which registers against the process-global registry and panics on a duplicate
// — a test calling it twice would take the whole package down. Building the vectors
// directly keeps the counters observable while leaving the global registry untouched.
//
// The cost is that this mirrors the constructor's label sets instead of sharing them,
// which is a drift risk. It is worth paying: without SOME observer, every recorder in this
// package is a no-op under test and a mutation that deletes a recording survives silently.
// That was measured, not assumed — removing the per-refusal loop from recordBatchOutcome
// killed no test at all.
func observableBatchMetrics() *BatchMetrics {
	return &BatchMetrics{
		enqueues: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "batch_enqueues_total"},
			[]string{"target_kind", "outcome"}),
		devices: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "batch_devices_total"},
			[]string{"disposition"}),
		refusals: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "batch_refusals_total"},
			[]string{"code", "bound"}),
		cancels: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "batch_cancel_commands_total"},
			[]string{"disposition"}),
	}
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	return testutil.ToFloat64(vec.WithLabelValues(labels...))
}

// TestRecordBatchOutcomeActuallyWritesTheCounters closes the hole every other test in this
// package leaves open: they all run with a nil BatchMetrics, so the recording calls are
// no-ops and a mutation that deletes one is invisible.
func TestRecordBatchOutcomeActuallyWritesTheCounters(t *testing.T) {
	t.Run("a created batch counts its devices and each refusal", func(t *testing.T) {
		metrics := observableBatchMetrics()
		api := &Api{BatchMetrics: metrics}
		acct := &batchAccounting{targetKind: targetKindGroupLabel, accepted: 7}
		acct.refuse("COMMAND_NOT_IN_VOCABULARY", boundNone)
		acct.refuse(RejectHeldCeilingExceeded, boundReserve)

		api.recordBatchOutcome(acct, nil)

		if got := counterValue(t, metrics.enqueues, targetKindGroupLabel, outcomeCreated); got != 1 {
			t.Errorf("enqueues{group,created} = %v, want 1", got)
		}
		if got := counterValue(t, metrics.devices, dispositionAccepted); got != 7 {
			t.Errorf("devices{accepted} = %v, want 7", got)
		}
		if got := counterValue(t, metrics.devices, dispositionRefused); got != 2 {
			t.Errorf("devices{refused} = %v, want 2", got)
		}
		if got := counterValue(t, metrics.refusals, "COMMAND_NOT_IN_VOCABULARY", boundNone); got != 1 {
			t.Errorf("refusals{vocabulary} = %v, want 1 — the per-refusal loop wrote nothing", got)
		}
		if got := counterValue(t, metrics.refusals, string(RejectHeldCeilingExceeded), boundReserve); got != 1 {
			t.Errorf("refusals{ceiling,reserve} = %v, want 1", got)
		}
	})

	t.Run("a replay counts once and nothing else", func(t *testing.T) {
		metrics := observableBatchMetrics()
		api := &Api{BatchMetrics: metrics}
		api.recordBatchOutcome(&batchAccounting{
			targetKind: targetKindDeviceListLabel, replay: true, accepted: 500,
		}, nil)

		if got := counterValue(t, metrics.enqueues, targetKindDeviceListLabel, outcomeReplayed); got != 1 {
			t.Errorf("enqueues{replayed} = %v, want 1", got)
		}
		// 🔴 A replay wrote no command rows. Counting its accepted devices would report a
		// fleet-sized admission for a call that created nothing — which is exactly what
		// the losing side of a create race used to do.
		if got := counterValue(t, metrics.devices, dispositionAccepted); got != 0 {
			t.Errorf("devices{accepted} = %v after a replay, want 0", got)
		}
	})

	t.Run("undecided is counted apart from refused, and emits nothing else", func(t *testing.T) {
		metrics := observableBatchMetrics()
		api := &Api{BatchMetrics: metrics}
		acct := &batchAccounting{targetKind: targetKindGroupLabel, accepted: 3}
		api.recordBatchOutcome(acct, errors.New("device-management is unreachable"))

		if got := counterValue(t, metrics.enqueues, targetKindGroupLabel, outcomeUndecided); got != 1 {
			t.Errorf("enqueues{undecided} = %v, want 1", got)
		}
		if got := counterValue(t, metrics.enqueues, targetKindGroupLabel, outcomeRefused); got != 0 {
			t.Errorf("an outage was counted as a refusal (%v); a spike in one reading as a "+
				"spike in the other is how a platform failure gets diagnosed as tenant abuse", got)
		}
		if got := counterValue(t, metrics.devices, dispositionAccepted); got != 0 {
			t.Errorf("devices{accepted} = %v for an undecided batch, want 0", got)
		}
	})

	t.Run("a request-shaped refusal is classified from the error", func(t *testing.T) {
		metrics := observableBatchMetrics()
		api := &Api{BatchMetrics: metrics}
		acct := &batchAccounting{targetKind: targetKindUnknownLabel}
		api.recordBatchOutcome(acct, rejected(RejectBatchTargetAmbiguous, "both or neither"))

		if got := counterValue(t, metrics.enqueues, targetKindUnknownLabel, outcomeRefused); got != 1 {
			t.Errorf("enqueues{unknown,refused} = %v, want 1", got)
		}
		if got := counterValue(t, metrics.refusals, string(RejectBatchTargetAmbiguous), boundNone); got != 1 {
			t.Errorf("refusals{ambiguous} = %v, want 1 — a refusal that names no device "+
				"still has to say WHY the fleet write was refused", got)
		}
	})
}

// TestWholeBatchReserveAttributionAtTheCallSite exercises the DECISION, not the predicate.
//
// 🔴 THIS EXISTS BECAUSE THE TABLE TEST BELOW COULD NOT SEE THE BUG IT WAS WRITTEN FOR.
// The predicate was always correct; the defect was which POSITION the whole-batch refusal
// handed it, and a table over refusedByReserve passes identically either way. Reverting
// the fix and watching only THIS test fail is what proves the coverage is real.
//
// It drives decideCommandBatch directly, because the accounting carries the attribution
// and is deliberately not reachable through the returned batch.
func TestWholeBatchReserveAttributionAtTheCallSite(t *testing.T) {
	devices := func(n int) *[]string {
		tokens := make([]string, 0, n)
		for i := 0; i < n; i++ {
			tokens = append(tokens, fmt.Sprintf("dev-%04d", i))
		}
		return &tokens
	}

	cases := []struct {
		name  string
		count int
		want  string
	}{
		// Ceiling 100, 20% reserved: this caller may hold 80.
		{"over the limit but inside the ceiling is the reserve's doing", 90, boundReserve},
		{"over the ceiling too would have been refused anyway", 120, boundCeiling},
		{"exactly at the ceiling still fits without the reserve", 100, boundReserve},
		{"one past the ceiling does not", 101, boundCeiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newBatchTestApi(t)
			api.DefaultHeldCommandCeiling = 100
			api.DeliveryMachineryReserve = 0.20
			ctx := core.WithTenant(context.Background(), "A")

			acct := batchAccounting{}
			request := &CommandBatchCreateRequest{
				Token: "push", Name: "reboot", DeviceTokens: devices(tc.count),
			}
			_, err := api.decideCommandBatch(ctx, request, &acct)
			if err == nil {
				t.Fatalf("a batch of %d against a limit of 80 must be refused", tc.count)
			}
			if acct.refusalTotal != 1 {
				t.Fatalf("recorded %d refusal events, want exactly 1 for a whole-batch "+
					"refusal", acct.refusalTotal)
			}
			want := refusalRecord{code: RejectHeldCeilingExceeded, bound: tc.want}
			if acct.refusals[want] != 1 {
				t.Errorf("recorded %v, want a single {%s, %s}",
					acct.refusals, RejectHeldCeilingExceeded, tc.want)
			}
		})
	}
}

// TestReserveAttributionAtHeadroomShapesTheCallSiteCannotReach covers the two headroom
// shapes the call-site test above cannot construct through a real request.
//
// 🔑 IT DELIBERATELY DOES NOT RE-TEST THE FOUR BOUNDARIES. Those are exercised end to end
// by the call-site test, which is where the bug actually lived — this predicate was never
// wrong, only fed the wrong position. Duplicating its cases here would add a second place
// to update and a false sense that the predicate is what needs guarding.
func TestReserveAttributionAtHeadroomShapesTheCallSiteCannotReach(t *testing.T) {
	t.Run("a service token has no reserve to blame", func(t *testing.T) {
		// A platform service token draws on the whole ceiling, so the two headrooms are
		// equal and no refusal can ever be the reserve's.
		whole := batchHeadroom{underLimit: 10000, underCeiling: 10000}
		for _, position := range []int{0, 9999, 10000, 25000} {
			if got := whole.boundAt(position); got != boundCeiling {
				t.Errorf("position %d = %q; a reserve that does not apply cannot be blamed",
					position, got)
			}
		}
	})

	t.Run("a tenant already over its limit still distinguishes the two", func(t *testing.T) {
		// 9,000 outstanding against 8,000/10,000: no room under the limit at all, but
		// 1,000 under the raw ceiling. A batch of 500 is refused purely by the reserve.
		over := batchHeadroom{underLimit: 0, underCeiling: 1000}
		if got := over.boundAt(500 - 1); got != boundReserve {
			t.Errorf("a batch fitting the ceiling's remaining room = %q, want reserve", got)
		}
		if got := over.boundAt(2000 - 1); got != boundCeiling {
			t.Errorf("a batch exceeding the ceiling's remaining room = %q, want ceiling", got)
		}
	})
}

// TestRefusalCodeLabelsAreClosed pins the folding, in both directions.
//
// 🔑 THE SECOND HALF IS THE LOAD-BEARING ONE. Folding everything to "other" would satisfy
// a test that only checks unknown codes, and would also destroy the counter's usefulness —
// so the known codes have to be shown passing through unchanged.
func TestRefusalCodeLabelsAreClosed(t *testing.T) {
	t.Run("codes this build knows pass through", func(t *testing.T) {
		for _, code := range []RejectionCode{
			RejectHeldCeilingExceeded, RejectBatchTooLarge, RejectBatchPartialRefused,
			RejectGroupUnusable, RejectPayloadNotJSON,
		} {
			if got := metricCodeLabel(code); got != string(code) {
				t.Errorf("metricCodeLabel(%s) = %q, want it unchanged", code, got)
			}
		}
	})

	t.Run("codes relayed from the device owner pass through", func(t *testing.T) {
		// These are produced by another service and deliberately not re-declared as
		// constants here. They are allowlisted by value so the most common refusals a
		// heterogeneous fleet write produces are not all lumped into "other".
		for _, code := range []RejectionCode{"COMMAND_NOT_IN_VOCABULARY", "DEVICE_NOT_FOUND"} {
			if got := metricCodeLabel(code); got != string(code) {
				t.Errorf("metricCodeLabel(%s) = %q, want it unchanged", code, got)
			}
		}
	})

	t.Run("anything else folds, whatever it looks like", func(t *testing.T) {
		// A peer's future code, and a hostile one. Both must land in the same bucket:
		// the label is what bounds this counter's cardinality, and a value that reaches
		// it unfiltered is an unbounded label wearing a bounded one's clothes.
		for _, code := range []RejectionCode{
			"SOME_FUTURE_CODE",
			"",
			RejectionCode(string(make([]byte, 300))),
		} {
			if got := metricCodeLabel(code); got != codeOtherLabel {
				t.Errorf("metricCodeLabel(%q) = %q, want %q", code, got, codeOtherLabel)
			}
		}
	})
}

// TestTargetKindLabelsCoverEveryShape. The `unknown` value is not a fallback nobody
// reaches — it is what an ambiguous request reports, and an ambiguous request is a
// refusal the platform raises often enough to have its own code.
func TestTargetKindLabelsCoverEveryShape(t *testing.T) {
	tokens := []string{"a"}
	group := "group-1"
	empty := ""

	cases := []struct {
		name    string
		request CommandBatchCreateRequest
		want    string
	}{
		{"a device list", CommandBatchCreateRequest{DeviceTokens: &tokens}, targetKindDeviceListLabel},
		{"a group", CommandBatchCreateRequest{GroupToken: &group}, targetKindGroupLabel},
		{"both, which is refused", CommandBatchCreateRequest{DeviceTokens: &tokens, GroupToken: &group}, targetKindUnknownLabel},
		{"neither, which is also refused", CommandBatchCreateRequest{}, targetKindUnknownLabel},
		{"an empty group token is not a group", CommandBatchCreateRequest{GroupToken: &empty}, targetKindUnknownLabel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestTargetKindLabel(&tc.request); got != tc.want {
				t.Errorf("requestTargetKindLabel = %q, want %q", got, tc.want)
			}
		})
	}

	if got := targetKindLabel(BatchTargetKind("SOMETHING_ELSE")); got != targetKindUnknownLabel {
		t.Errorf("an unrecognised stored kind labelled %q; a value read back from a row "+
			"must not be able to invent a label", got)
	}
}

// TestNilBatchMetricsRecordNothing. Every test in this package builds an Api by literal,
// so a recorder that dereferenced a nil receiver would panic across the whole suite —
// but the reason it is nil is worth pinning: promauto registers against the global
// registry and panics on a duplicate, so tests must not register at all.
func TestNilBatchMetricsRecordNothing(t *testing.T) {
	if got := NewBatchMetrics(nil); got != nil {
		t.Fatal("NewBatchMetrics(nil) returned a registered metric set; a test Api would " +
			"then register against the global registry, and the second one would panic")
	}
	var metrics *BatchMetrics
	metrics.recordEnqueue(targetKindGroupLabel, outcomeCreated)
	metrics.recordDevices(dispositionAccepted, 5)
	metrics.recordRefusal(RejectBatchTooLarge, boundNone, 1)
	metrics.recordCancel(cancelDispositionCancelled, 3)
}

// TestPerDeviceReserveAttributionUnderAllowPartial covers the OTHER attribution site.
//
// 🔴 A MUTATION HARNESS FOUND THIS GAP, NOT A REVIEW. Off-by-one-ing the per-device probe
// survived every test in the package: the whole-batch test above exercises a different
// call site, and nothing else looked at the allowPartial path's labels at all.
//
// The case is the one the design exists for — a batch that STRADDLES both bounds. With a
// limit of 80 inside a ceiling of 100, a 120-device partial batch admits 80, and of the 40
// it refuses, exactly 20 would have fitted without the reserve and 20 would not. A single
// verdict for the whole batch could only ever be half right.
func TestPerDeviceReserveAttributionUnderAllowPartial(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 100
	api.DeliveryMachineryReserve = 0.20
	ctx := core.WithTenant(context.Background(), "A")

	tokens := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		tokens = append(tokens, fmt.Sprintf("dev-%04d", i))
	}

	acct := batchAccounting{}
	created, err := api.decideCommandBatch(ctx, &CommandBatchCreateRequest{
		Token: "partial-push", Name: "reboot", DeviceTokens: &tokens, AllowPartial: true,
	}, &acct)
	if err != nil {
		t.Fatalf("a partial batch must be admitted up to the headroom: %v", err)
	}
	if created.Accepted != 80 {
		t.Fatalf("accepted %d, want 80 (the limit, not the ceiling)", created.Accepted)
	}

	byReserve := acct.refusals[refusalRecord{code: RejectHeldCeilingExceeded, bound: boundReserve}]
	byCeiling := acct.refusals[refusalRecord{code: RejectHeldCeilingExceeded, bound: boundCeiling}]
	if byReserve != 20 {
		t.Errorf("%d devices attributed to the reserve, want 20 — positions 80..99 would "+
			"have fitted against the raw ceiling of 100", byReserve)
	}
	if byCeiling != 20 {
		t.Errorf("%d devices attributed to the ceiling, want 20 — positions 100..119 were "+
			"over the raw ceiling and the reserve had nothing to do with them", byCeiling)
	}
	if acct.refusalTotal != 40 {
		t.Errorf("refusalTotal = %d, want 40", acct.refusalTotal)
	}
}

// TestConflictedCreateCountsAsAReplay stages the losing side of a create race.
//
// 🔴 IT IS STAGED WITH A CALLBACK BECAUSE THE PUBLIC API CANNOT REACH IT. The replay probe
// runs first, so a batch token that already exists is answered there; the conflicted branch
// is only reachable when the row appears BETWEEN that probe and the insert. The callback
// inserts it in exactly that window.
//
// What it pins is a metrics fix, not a data fix: the loser returns the winner's batch with
// no error, so before this it fell through to the "created" branch and reported a second
// creation plus its own would-be admission as accepted devices — none of which were ever
// written.
func TestConflictedCreateCountsAsAReplay(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	db := api.RDB.Database

	// The winner, inserted from a separate statement just before ours lands.
	planted := false
	steal := func(tx *gorm.DB) {
		if planted || tx.Statement == nil || tx.Statement.Table != "command_batches" {
			return
		}
		planted = true
		winner := &CommandBatch{Name: "reboot", TargetKind: BatchTargetGroup.String()}
		winner.Token = "contested"
		winner.TenantId = "A"
		tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Create(winner)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("test:steal", steal); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	defer func() {
		if err := db.Callback().Create().Remove("test:steal"); err != nil {
			t.Fatalf("remove callback: %v", err)
		}
	}()

	acct := batchAccounting{}
	tokens := []string{"pump-a", "pump-b"}
	got, err := api.decideCommandBatch(ctx, &CommandBatchCreateRequest{
		Token: "contested", Name: "reboot", DeviceTokens: &tokens,
	}, &acct)
	if err != nil {
		t.Fatalf("losing the race is not an error: %v", err)
	}
	if !planted {
		t.Fatal("the callback never fired; this test is not staging a race at all")
	}
	if got.TargetKind != BatchTargetGroup.String() {
		t.Fatalf("returned a batch with kind %q; the loser must be handed the WINNER's "+
			"batch, which is the group one the callback planted", got.TargetKind)
	}
	if !acct.replay {
		t.Error("the losing side was not counted as a replay, so it reports a second " +
			"`created` for one real batch — and its own accepted devices, none of which " +
			"were written")
	}
}
