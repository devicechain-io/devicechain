// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

const testMinAccepted = 1000

// findInvariant returns the named invariant from a Reconcile result, failing the
// test if it is absent.
func findInvariant(t *testing.T, invs []Invariant, name string) Invariant {
	t.Helper()
	for _, inv := range invs {
		if inv.Name == name {
			return inv
		}
	}
	t.Fatalf("invariant %q not present in %v", name, invs)
	return Invariant{}
}

func TestReconcile(t *testing.T) {
	cases := []struct {
		name            string
		accepted        int64
		failed          int64
		persisted       int64
		wantLoadApplied bool
		wantCleanDrive  bool
		wantComplete    bool
		wantOverall     bool
	}{
		{
			name:     "exact match on a real load passes everything",
			accepted: 4000, failed: 0, persisted: 4000,
			wantLoadApplied: true, wantCleanDrive: true, wantComplete: true, wantOverall: true,
		},
		{
			name:     "a single dropped event fails completeness",
			accepted: 4000, failed: 0, persisted: 3999,
			wantLoadApplied: true, wantCleanDrive: true, wantComplete: false, wantOverall: false,
		},
		{
			name:     "one extra persisted event fails completeness",
			accepted: 4000, failed: 0, persisted: 4001,
			wantLoadApplied: true, wantCleanDrive: true, wantComplete: false, wantOverall: false,
		},
		{
			// finding 3: a run below the load floor must not certify "under load".
			name:     "below the load floor fails, even reconciling exactly",
			accepted: 500, failed: 0, persisted: 500,
			wantLoadApplied: false, wantCleanDrive: true, wantComplete: false, wantOverall: false,
		},
		{
			// The check that cannot fail: 0 accepted is below the floor and vacuous.
			name:     "zero accepted fails the floor and is inconclusive",
			accepted: 0, failed: 0, persisted: 0,
			wantLoadApplied: false, wantCleanDrive: true, wantComplete: false, wantOverall: false,
		},
		{
			// finding 1: a failed emit may have persisted server-side (timeout after
			// 202), so persisted == accepted here could be a coincidence masking a
			// real drop. A non-clean drive is inconclusive — never a pass.
			name:     "failed emits make even an exact match inconclusive",
			accepted: 4000, failed: 5, persisted: 4000,
			wantLoadApplied: true, wantCleanDrive: false, wantComplete: false, wantOverall: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invs := Reconcile(tc.accepted, tc.failed, tc.persisted, testMinAccepted)
			if got := findInvariant(t, invs, InvLoadApplied).Passed; got != tc.wantLoadApplied {
				t.Errorf("load-applied = %v, want %v", got, tc.wantLoadApplied)
			}
			if got := findInvariant(t, invs, InvCleanDrive).Passed; got != tc.wantCleanDrive {
				t.Errorf("clean-drive = %v, want %v", got, tc.wantCleanDrive)
			}
			if got := findInvariant(t, invs, InvCompleteness).Passed; got != tc.wantComplete {
				t.Errorf("completeness = %v, want %v", got, tc.wantComplete)
			}
			r := &Report{Invariants: invs}
			if got := r.Passed(); got != tc.wantOverall {
				t.Errorf("overall = %v, want %v", got, tc.wantOverall)
			}
		})
	}
}

// scriptedCounter returns values[call], plateauing at the last value once the
// script is exhausted; it can also inject a read error on a chosen call.
type scriptedCounter struct {
	values []int64
	errAt  int // 1-based call index to fail on; 0 = never
	calls  int
}

func (s *scriptedCounter) Count(_ context.Context, _ Window) (int64, error) {
	s.calls++
	if s.errAt > 0 && s.calls == s.errAt {
		return 0, errors.New("read-back boom")
	}
	i := s.calls - 1
	if i >= len(s.values) {
		i = len(s.values) - 1
	}
	return s.values[i], nil
}

func TestAwaitReachesTarget(t *testing.T) {
	o := &Oracle{Counter: &scriptedCounter{values: []int64{10, 500, 900, 1000}}, Poll: 0, Timeout: time.Second}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1000 {
		t.Fatalf("expected reached at 1000, got %+v", qr)
	}
}

func TestAwaitOvershootReachesExit(t *testing.T) {
	// A count above the target (contamination) still reaches the exit so Reconcile
	// can flag it — it must not spin forever waiting to equal the target exactly.
	o := &Oracle{Counter: &scriptedCounter{values: []int64{10, 1001}}, Poll: 0, Timeout: time.Second}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1001 {
		t.Fatalf("expected reached at 1001, got %+v", qr)
	}
}

func TestAwaitBelowTargetTimesOut(t *testing.T) {
	// A count that never reaches the target (a real drop, or lag that never
	// drained) must return Reached=false — never a premature "settled" pass. It
	// keeps polling to the timeout rather than concluding early on a plateau.
	o := &Oracle{Counter: &scriptedCounter{values: []int64{999}}, Poll: 0, Timeout: 30 * time.Millisecond}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qr.Reached {
		t.Fatalf("expected not reached at 999<1000, got %+v", qr)
	}
	if qr.Persisted != 999 {
		t.Fatalf("expected last count 999, got %d", qr.Persisted)
	}
}

func TestAwaitReadErrorFailsClosed(t *testing.T) {
	// A read-back error must surface as a run error — never be swallowed into a
	// zero count that the reconcile would read as "everything dropped".
	o := &Oracle{Counter: &scriptedCounter{values: []int64{100, 100}, errAt: 2}, Poll: 0, Timeout: time.Second}
	_, err := o.Await(context.Background(), Window{}, 1000)
	if err == nil {
		t.Fatal("expected a read-back error to propagate, got nil")
	}
}

func TestAwaitSettleCatchesLateOvershoot(t *testing.T) {
	// The blind spot the settle re-count closes: the count reaches the target, then
	// a late duplicate over-persists it. Without the settle phase Await would exit at
	// first-reach reporting 1000 == accepted — a false completeness pass while the
	// true state carries an extra row. With Settle > 0 it keeps watching and returns
	// the MAX (1001), which Reconcile flags as persisted > accepted.
	//
	// Poll 0 makes the outcome timing-independent: the tight settle loop consumes
	// the scripted 1001 in microseconds, and the counter plateaus there, so the
	// returned value is 1001 no matter how many spins the wall-clock window allows.
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{1000, 1000, 1001}},
		Poll:    0, Timeout: time.Second, Settle: 20 * time.Millisecond,
	}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached {
		t.Fatalf("expected reached, got %+v", qr)
	}
	if qr.Persisted != 1001 {
		t.Fatalf("settle must surface the late over-persist as 1001, got %d", qr.Persisted)
	}
	// Mutation guard: comp must FAIL on the settled count (1001 > 1000 accepted).
	comp := findInvariant(t, Reconcile(1000, 0, qr.Persisted, 10), InvCompleteness)
	if comp.Passed {
		t.Fatalf("completeness must fail on a late over-persist, got pass: %s", comp.Detail)
	}
}

func TestAwaitSettleTracksMaxNotLast(t *testing.T) {
	// The settle reports the MAXIMUM count seen, not the last read. A transient
	// overshoot that later reads lower (e.g. an out-of-band delete after a duplicate
	// landed) must still be surfaced — a duplicate that briefly existed is a real
	// corruption finding. Pins the max-tracking against a `maxSeen = count`
	// regression that a monotone-only script would not catch.
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{1000, 1001, 1000}},
		Poll:    0, Timeout: time.Second, Settle: 20 * time.Millisecond,
	}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1001 {
		t.Fatalf("settle must report the max (1001) even though the count read back down to 1000, got %+v", qr)
	}
}

func TestAwaitSettleConfirmsWhenShorterThanPoll(t *testing.T) {
	// A settle shorter than one poll interval must still make ONE confirming read
	// rather than degrading to a no-op: the deadline is checked AFTER the read. Here
	// Settle (1ns) << Poll (1ms), so the loop sleeps one poll, reads the late 1001,
	// then exits — the over-persist is still caught. Pins that the deadline check
	// stays after the read.
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{1000, 1001}},
		Poll:    time.Millisecond, Timeout: time.Second, Settle: time.Nanosecond,
	}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1001 {
		t.Fatalf("a sub-poll settle must still confirm once and catch 1001, got %+v", qr)
	}
}

func TestAwaitSettleStableReturnsTarget(t *testing.T) {
	// The healthy case: the count reaches the target and stays there through the
	// settle window. The re-count must not invent an overshoot — it returns exactly
	// the target so completeness passes.
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{1000}},
		Poll:    0, Timeout: time.Second, Settle: 20 * time.Millisecond,
	}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1000 {
		t.Fatalf("expected a stable reach at 1000, got %+v", qr)
	}
}

func TestAwaitZeroSettleExitsOnFirstReach(t *testing.T) {
	// Settle == 0 preserves the historic exit-on-first-reach: the count reaches the
	// target and Await returns immediately, so a duplicate that would land AFTER
	// (1002, never read) is deliberately out of scope. This pins that a zero settle
	// is a true no-op — the mode selftest's controls run in (they construct the
	// Oracle without a Settle).
	o := &Oracle{Counter: &scriptedCounter{values: []int64{1000, 1002}}, Poll: 0, Timeout: time.Second}
	qr, err := o.Await(context.Background(), Window{}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Reached || qr.Persisted != 1000 {
		t.Fatalf("zero settle must exit at the first reach (1000), got %+v", qr)
	}
}

func TestAwaitSettleReadErrorFailsClosed(t *testing.T) {
	// A read-back error DURING the settle phase must surface as a run error too —
	// never be swallowed into a quiet "no overshoot" that would let a duplicate hide
	// behind a failed read.
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{1000, 1000}, errAt: 2},
		Poll:    0, Timeout: time.Second, Settle: 20 * time.Millisecond,
	}
	if _, err := o.Await(context.Background(), Window{}, 1000); err == nil {
		t.Fatal("expected a settle-phase read error to propagate, got nil")
	}
}

func TestHTTPGraphQLFromWS(t *testing.T) {
	cases := map[string]string{
		"ws://host:8080/api/event-management/graphql": "http://host:8080/api/event-management/graphql",
		"wss://host/api/event-management/graphql":     "https://host/api/event-management/graphql",
		"http://host/api/event-management/graphql":    "http://host/api/event-management/graphql",
	}
	for in, want := range cases {
		got, err := httpGraphQLFromWS(in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if _, err := httpGraphQLFromWS("ftp://nope"); err == nil {
		t.Error("expected an error on an unexpected scheme")
	}
}

func TestDeriveWindow(t *testing.T) {
	start := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Second)
	w := deriveWindow(start, end)
	// The window must fully contain [start, end] with a small pad on each side —
	// never tighter (that would drop boundary events) and not minutes-wide (that
	// would widen the contamination surface the clean-tenant guard protects).
	if !w.Start.Before(start) || start.Sub(w.Start) > 30*time.Second {
		t.Errorf("start pad off: window start %s vs drive start %s", w.Start, start)
	}
	if !w.End.After(end) || w.End.Sub(end) > 30*time.Second {
		t.Errorf("end pad off: window end %s vs drive end %s", w.End, end)
	}
}

func TestReportPassed(t *testing.T) {
	// An empty invariant set has proven nothing → not a pass.
	if (&Report{}).Passed() {
		t.Error("empty report must not pass")
	}
	pass := &Report{Invariants: []Invariant{{Name: "a", Passed: true}, {Name: "b", Passed: true}}}
	if !pass.Passed() {
		t.Error("all-passing report should pass")
	}
	fail := &Report{Invariants: []Invariant{{Name: "a", Passed: true}, {Name: "b", Passed: false}}}
	if fail.Passed() {
		t.Error("one failed invariant must fail the report")
	}
}

// TestGraphqlEventCounter exercises the real read-back path against a stub GraphQL
// server: it must send the Measurement eventType filter, read totalRecords as the
// count, and — the load-bearing fail-closed property — treat a null totalRecords
// as an error, never a silent zero.
func TestGraphqlEventCounter(t *testing.T) {
	newCounter := func(t *testing.T, totalRecords string) (*graphqlEventCounter, *[]string) {
		t.Helper()
		var eventBodies []string
		future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body := string(raw)
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(body, "login("):
				_, _ = w.Write([]byte(`{"data":{"login":{"identityToken":"id","expiresAt":"` + future + `","superuser":true,"memberships":[{"tenant":"t","roles":["r"]}]}}}`))
			case strings.Contains(body, "selectTenant("):
				_, _ = w.Write([]byte(`{"data":{"selectTenant":{"accessToken":"acc","refreshToken":"ref","expiresAt":"` + future + `"}}}`))
			case strings.Contains(body, "events("):
				eventBodies = append(eventBodies, body)
				_, _ = w.Write([]byte(`{"data":{"events":{"pagination":{"totalRecords":` + totalRecords + `}}}}`))
			default:
				t.Errorf("unexpected request body: %s", body)
			}
		}))
		t.Cleanup(srv.Close)
		session := userclient.NewTenantSession(srv.Client(), srv.URL, "e@x", "pw", "t")
		return &graphqlEventCounter{session: session, endpoint: srv.URL}, &eventBodies
	}

	t.Run("counts and filters to Measurement", func(t *testing.T) {
		c, bodies := newCounter(t, "4000")
		got, err := c.Count(context.Background(), Window{Start: time.Now().Add(-time.Minute), End: time.Now()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 4000 {
			t.Fatalf("count = %d, want 4000", got)
		}
		if len(*bodies) != 1 || !strings.Contains((*bodies)[0], "\"eventTypes\":[2]") {
			t.Fatalf("events query did not carry the Measurement eventType filter: %v", *bodies)
		}
	})

	t.Run("null totalRecords fails closed", func(t *testing.T) {
		c, _ := newCounter(t, "null")
		_, err := c.Count(context.Background(), Window{Start: time.Now().Add(-time.Minute), End: time.Now()})
		if err == nil {
			t.Fatal("a null totalRecords must be an error, not a silent zero")
		}
	})
}
