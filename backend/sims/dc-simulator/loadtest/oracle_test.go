// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
		name              string
		accepted          int64
		persisted         int64
		settled           bool
		wantLoadApplied   bool
		wantQuiesced      bool
		wantCompleteness  bool
		wantOverallPassed bool
	}{
		{
			name:     "exact match passes everything",
			accepted: 100, persisted: 100, settled: true,
			wantLoadApplied: true, wantQuiesced: true, wantCompleteness: true, wantOverallPassed: true,
		},
		{
			name:     "a single dropped event fails completeness",
			accepted: 100, persisted: 99, settled: true,
			wantLoadApplied: true, wantQuiesced: true, wantCompleteness: false, wantOverallPassed: false,
		},
		{
			name:     "one extra persisted event fails completeness",
			accepted: 100, persisted: 101, settled: true,
			wantLoadApplied: true, wantQuiesced: true, wantCompleteness: false, wantOverallPassed: false,
		},
		{
			// The check that cannot fail: 0 == 0 must NOT pass. Without the
			// load-applied guard a run that emitted nothing would read green.
			name:     "zero accepted is vacuous and must fail",
			accepted: 0, persisted: 0, settled: true,
			wantLoadApplied: false, wantQuiesced: true, wantCompleteness: false, wantOverallPassed: false,
		},
		{
			// A coincidental equality on an unsettled pipeline must not pass:
			// the count is still moving, so equality here is meaningless.
			name:     "equal but not quiesced fails (inconclusive)",
			accepted: 100, persisted: 100, settled: false,
			wantLoadApplied: true, wantQuiesced: false, wantCompleteness: false, wantOverallPassed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invs := Reconcile(tc.accepted, tc.persisted, tc.settled)
			if got := findInvariant(t, invs, InvLoadApplied).Passed; got != tc.wantLoadApplied {
				t.Errorf("load-applied passed = %v, want %v", got, tc.wantLoadApplied)
			}
			if got := findInvariant(t, invs, InvQuiesced).Passed; got != tc.wantQuiesced {
				t.Errorf("quiesced passed = %v, want %v", got, tc.wantQuiesced)
			}
			if got := findInvariant(t, invs, InvCompleteness).Passed; got != tc.wantCompleteness {
				t.Errorf("completeness passed = %v, want %v", got, tc.wantCompleteness)
			}
			r := &Report{Invariants: invs}
			if got := r.Passed(); got != tc.wantOverallPassed {
				t.Errorf("overall passed = %v, want %v", got, tc.wantOverallPassed)
			}
		})
	}
}

func TestPlateauObserve(t *testing.T) {
	// Rising then flat: settles only after `stable` consecutive equal reads.
	var p plateau
	stable := 3
	seq := []int64{10, 50, 90, 100, 100, 100}
	settledAt := -1
	for i, v := range seq {
		if p.observe(v, stable) && settledAt < 0 {
			settledAt = i
		}
	}
	// Reads at indices 3,4,5 are all 100 → the third equal read (index 5) settles.
	if settledAt != 5 {
		t.Fatalf("settled at read %d, want 5", settledAt)
	}

	// A change resets the streak: 100,100,101,101,101 settles on the 3rd 101.
	p = plateau{}
	seq = []int64{100, 100, 101, 101, 101}
	settledAt = -1
	for i, v := range seq {
		if p.observe(v, stable) && settledAt < 0 {
			settledAt = i
		}
	}
	if settledAt != 4 {
		t.Fatalf("post-change settled at read %d, want 4", settledAt)
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

func TestAwaitSettlesAtPlateau(t *testing.T) {
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{10, 500, 900, 1000, 1000, 1000}},
		Poll:    0, // tight loop, no wall-clock wait
		Stable:  3,
		Timeout: time.Second,
	}
	qr, err := o.Await(context.Background(), Window{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !qr.Settled {
		t.Fatalf("expected settled, got %+v", qr)
	}
	if qr.Persisted != 1000 {
		t.Fatalf("persisted = %d, want 1000", qr.Persisted)
	}
}

func TestAwaitReadErrorFailsClosed(t *testing.T) {
	// A read-back error must surface as a run error — never be swallowed into a
	// zero count that the reconcile would read as "everything dropped".
	o := &Oracle{
		Counter: &scriptedCounter{values: []int64{100, 100}, errAt: 2},
		Poll:    0,
		Stable:  3,
		Timeout: time.Second,
	}
	_, err := o.Await(context.Background(), Window{})
	if err == nil {
		t.Fatal("expected a read-back error to propagate, got nil")
	}
}

// growingCounter never returns the same value twice, so it never plateaus — the
// only way Await can exit is the timeout.
type growingCounter struct{ calls int64 }

func (g *growingCounter) Count(_ context.Context, _ Window) (int64, error) {
	g.calls++
	return g.calls, nil
}

func TestAwaitNeverSettlesTimesOut(t *testing.T) {
	// A count that never settles (lag that never drains) must return
	// Settled=false, which Reconcile turns into a failed invariant — a scale
	// finding, not a pass.
	o := &Oracle{
		Counter: &growingCounter{},
		Poll:    0,
		Stable:  3,
		Timeout: 30 * time.Millisecond,
	}
	qr, err := o.Await(context.Background(), Window{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qr.Settled {
		t.Fatalf("expected not settled on ever-growing count, got %+v", qr)
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

func TestReportPassed(t *testing.T) {
	// An empty invariant set has proven nothing → not a pass.
	empty := &Report{}
	if empty.Passed() {
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
