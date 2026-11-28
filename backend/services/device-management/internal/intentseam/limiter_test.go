// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intentseam

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/intent"
)

func isBusy(err error) bool {
	var se *Error
	return errors.As(err, &se) && se.Code == CodeSeamBusy
}

// At most MaxConcurrentStatements statements hold a slot at once, at most MaxDigestStatements of
// them digests or intersections, and a statement that finds no slot within the wait is refused
// SEAM_BUSY rather than queued without bound.
func TestTheLimiterBoundsConcurrentStatements(t *testing.T) {
	if MaxConcurrentStatements != 4 || MaxDigestStatements != 2 {
		t.Fatalf("the seam's budgets moved: %d statements, %d digests", MaxConcurrentStatements, MaxDigestStatements)
	}
	s := NewWithBudgets(20*time.Millisecond, nil, 0)
	ctx := context.Background()
	var held []func()
	for _, op := range []string{OpDigest, OpIntersect} {
		release, err := s.acquire(ctx, op)
		if err != nil {
			t.Fatalf("heavy statement %s refused below the limit: %v", op, err)
		}
		held = append(held, release)
	}
	if _, err := s.acquire(ctx, OpDigest); !isBusy(err) {
		t.Fatalf("a third heavy statement = %v; want SEAM_BUSY", err)
	}
	for _, op := range []string{OpPage, OpPreview} {
		release, err := s.acquire(ctx, op)
		if err != nil {
			t.Fatalf("light statement %s refused while slots were free: %v", op, err)
		}
		held = append(held, release)
	}
	if _, err := s.acquire(ctx, OpPage); !isBusy(err) {
		t.Fatalf("a fifth statement = %v; want SEAM_BUSY", err)
	}
	// A refused heavy statement must not keep the digest slot it took before the shared slot
	// timed out.
	held[0]()
	filler, err := s.acquire(ctx, OpPage)
	if err != nil {
		t.Fatalf("a statement after a release = %v", err)
	}
	held = append(held, filler)
	if _, err := s.acquire(ctx, OpDigest); !isBusy(err) {
		t.Fatalf("a heavy statement with the shared slots full = %v; want SEAM_BUSY", err)
	}
	if got := len(s.heavy); got != 1 {
		t.Fatalf("after a refused heavy statement %d digest slots are held; want 1", got)
	}
	for _, r := range held[1:] {
		r()
	}
	if len(s.all) != 0 || len(s.heavy) != 0 {
		t.Fatalf("slots leaked: %d shared, %d digest", len(s.all), len(s.heavy))
	}
}

func TestTheLimiterWaitsUpToItsBudget(t *testing.T) {
	s := NewWithBudgets(time.Second, nil, 0)
	ctx := context.Background()
	var held []func()
	for range MaxConcurrentStatements {
		r, err := s.acquire(ctx, OpPage)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, r)
	}
	got := make(chan error, 1)
	go func() {
		r, err := s.acquire(ctx, OpPage)
		if err == nil {
			r()
		}
		got <- err
	}()
	held[0]() // a slot frees while the fifth is waiting
	if err := <-got; err != nil {
		t.Fatalf("a statement that a slot freed for within the wait = %v", err)
	}
	for _, r := range held[1:] {
		r()
	}
}

func TestUnknownStatementKindsAreRefused(t *testing.T) {
	if err := New().Run(context.Background(), nil, "walk", nil); err == nil {
		t.Fatal("an unknown statement kind ran")
	}
	var nilSeam *Seam
	if err := nilSeam.Run(context.Background(), nil, OpDigest, nil); err == nil {
		t.Fatal("an unwired seam answered")
	}
}

func TestEveryStatementHasItsBudget(t *testing.T) {
	want := map[string]time.Duration{OpDigest: 20 * time.Second, OpIntersect: 20 * time.Second, OpPage: 5 * time.Second, OpPreview: 30 * time.Second}
	got := New().timeouts
	if len(got) != len(want) {
		t.Fatalf("timeouts = %v", got)
	}
	for op, d := range want {
		if got[op] != d {
			t.Errorf("%s timeout = %s, want %s", op, got[op], d)
		}
	}
	if MaxMembers(New()) != intent.MaxDevicesPerIntent {
		t.Errorf("a digest refuses above %d members, not intent.MaxDevicesPerIntent", MaxMembers(New()))
	}
	if !heavyOp(OpDigest) || !heavyOp(OpIntersect) || heavyOp(OpPage) || heavyOp(OpPreview) {
		t.Error("the heavy statement classes moved")
	}
}
