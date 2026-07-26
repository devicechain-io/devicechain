// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/replication"
)

// The settle loop has three exits and only one of them is obvious. These pin all
// three, because the non-obvious one — the window expiring while COLLECTION was
// failing — is the difference between reporting "this instance is not replicated"
// and reporting "we could not find out", and getting it wrong records a fiction
// in a drill.

func fastSettle(t *testing.T) {
	t.Helper()
	prev := settleInterval
	settleInterval = time.Millisecond
	t.Cleanup(func() { settleInterval = prev })
}

func failing(findings int) replication.Report {
	rep := replication.Report{Replicas: 3}
	for i := 0; i < findings; i++ {
		rep.Findings = append(rep.Findings, replication.Finding{
			Check: "A1", Object: "some-stream", Message: "is configured for 1 replica(s), want 3",
		})
	}
	return rep
}

// TestSettleReturnsAsSoonAsItPasses covers the happy exit: a peer set that
// finishes catching up mid-window must end the loop, not run it out.
func TestSettleReturnsAsSoonAsItPasses(t *testing.T) {
	fastSettle(t)
	calls := 0
	rep, err := settleUntilOK(context.Background(), func() (replication.Report, error) {
		calls++
		if calls < 3 {
			return failing(1), nil
		}
		return replication.Report{Replicas: 3}, nil
	}, time.Minute)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("expected the settled pass; got %s", rep.Format())
	}
	if calls != 3 {
		t.Fatalf("expected the loop to stop at the first pass; it ran %d times", calls)
	}
}

// TestSettleReturnsTheLastReportWhenItNeverPasses pins that settling is not
// leniency. A persistent failure comes back with every finding intact.
func TestSettleReturnsTheLastReportWhenItNeverPasses(t *testing.T) {
	fastSettle(t)
	rep, err := settleUntilOK(context.Background(), func() (replication.Report, error) {
		return failing(4), nil
	}, 20*time.Millisecond)

	if err != nil {
		t.Fatalf("a persistent failure is a VERDICT, not an error: %v", err)
	}
	if rep.OK() {
		t.Fatal("the settle window must never turn a failure into a pass")
	}
	if len(rep.Findings) != 4 {
		t.Fatalf("every finding must survive the window; got %d", len(rep.Findings))
	}
}

// TestSettleRefusesToReportAStaleVerdict is the case that motivated splitting
// this out. Collection starts working and then breaks — a dropped tunnel, a
// restarted broker pod — and the window expires. The report in hand describes the
// broker BEFORE the breakage, and returning it would present a transport failure
// as a replication verdict.
func TestSettleRefusesToReportAStaleVerdict(t *testing.T) {
	fastSettle(t)
	calls := 0
	_, err := settleUntilOK(context.Background(), func() (replication.Report, error) {
		calls++
		if calls == 1 {
			return failing(1), nil
		}
		return replication.Report{}, errors.New("connection closed")
	}, 20*time.Millisecond)

	if err == nil {
		t.Fatal("the window expired while collection was FAILING; returning the stale " +
			"report as a verdict records 'not replicated' when the truth is 'could not " +
			"find out'")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("the error must say the report was discarded as stale; got %q", err)
	}
}

// TestSettleRecoversFromATransientCollectionError is the counterweight: one
// failed collection must not abort the window, or a single dropped request during
// a rollout ends the check.
func TestSettleRecoversFromATransientCollectionError(t *testing.T) {
	fastSettle(t)
	calls := 0
	rep, err := settleUntilOK(context.Background(), func() (replication.Report, error) {
		calls++
		switch calls {
		case 1:
			return failing(1), nil
		case 2:
			return replication.Report{}, errors.New("transient")
		default:
			return replication.Report{Replicas: 3}, nil
		}
	}, time.Minute)

	if err != nil {
		t.Fatalf("a single transient error must not end the window: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("expected the eventual pass; got %s", rep.Format())
	}
}

// TestSettleDisabledDoesNotRetry pins that --settle 0 means one attempt. A drill
// that wants the instantaneous state must be able to ask for it.
func TestSettleDisabledDoesNotRetry(t *testing.T) {
	calls := 0
	rep, err := settleUntilOK(context.Background(), func() (replication.Report, error) {
		calls++
		return failing(1), nil
	}, 0)

	if err != nil || rep.OK() || calls != 1 {
		t.Fatalf("settle=0 must make exactly one attempt and return its verdict; "+
			"calls=%d ok=%v err=%v", calls, rep.OK(), err)
	}
}

// TestSettleStopsOnContextCancel keeps the overall --timeout meaningful: the
// window must not outlive the context bounding the whole check.
func TestSettleStopsOnContextCancel(t *testing.T) {
	fastSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := settleUntilOK(ctx, func() (replication.Report, error) {
		return failing(1), nil
	}, time.Hour); err == nil {
		t.Fatal("a cancelled context must end the settle window")
	}
}
