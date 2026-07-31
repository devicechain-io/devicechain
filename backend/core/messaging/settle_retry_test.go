// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
)

// These tests exist because settleRetry is the one piece of the clustered-test
// harness that could silently turn a red gate green.
//
// 🔴 A BLANKET "retry until it works" AROUND A JETSTREAM CALL WOULD SWALLOW A
// PRODUCT REGRESSION, which is strictly worse than the intermittent failure it was
// added to remove — an intermittent gate at least still fails sometimes. So the
// helper retries ONLY the documented settling transients, and both of the ways it
// is supposed to give up are asserted here rather than assumed.

func TestSettleRetryClassifiesTheSettlingTransients(t *testing.T) {
	transient := []struct {
		name string
		err  error
	}{
		// The two identities actually observed failing CI, by identity rather than
		// by text, so a message reword upstream does not silently un-cover them.
		{"no stream response", nats.ErrNoStreamResponse},
		{"no responders", nats.ErrNoResponders},
		{"timeout", nats.ErrTimeout},
		{"context deadline", context.DeadlineExceeded},
		// Wrapped, because callers rarely hand back the bare sentinel.
		{"wrapped no stream response", fmt.Errorf("put k0: %w", nats.ErrNoStreamResponse)},
		{"wrapped context deadline", fmt.Errorf("adding durable: %w", context.DeadlineExceeded)},
		// Untyped, which is how some transport errors arrive.
		{"untyped no responders text", errors.New("nats: no responders available for request")},
	}
	for _, tc := range transient {
		t.Run(tc.name, func(t *testing.T) {
			if !jsGroupNotServingYet(tc.err) {
				t.Fatalf("%v must be treated as the settling window, or the flake it causes comes back", tc.err)
			}
		})
	}

	// The counterweight. Without this the classifier could return true for
	// everything and every test above would still pass.
	notTransient := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"stream not found", errors.New("nats: stream not found")},
		{"wrong last sequence", errors.New("nats: wrong last sequence: 1")},
		{"maximum bytes exceeded", errors.New("nats: maximum bytes exceeded")},
		{"insufficient resources", errors.New("nats: insufficient resources")},
		{"no suitable peers", errors.New("nats: no suitable peers for placement")},
		{"arbitrary", errors.New("boom")},
	}
	for _, tc := range notTransient {
		t.Run("not/"+tc.name, func(t *testing.T) {
			if jsGroupNotServingYet(tc.err) {
				t.Fatalf("%v must NOT be retried — retrying a real failure is how a gate stops being able to fail", tc.err)
			}
		})
	}
}

// TestSettleRetryFailsFastOnARealError is the mutation that matters: a genuine
// JetStream error must surface on the FIRST attempt, unchanged, rather than being
// retried for 30s and then reported as a settling timeout.
func TestSettleRetryFailsFastOnARealError(t *testing.T) {
	real := errors.New("nats: maximum bytes exceeded")
	attempts := 0
	start := time.Now()
	err := settleRetry(func() error {
		attempts++
		return real
	}, 30*time.Second, 50*time.Millisecond)

	if attempts != 1 {
		t.Fatalf("a non-transient error must not be retried; op ran %d times", attempts)
	}
	if !errors.Is(err, real) {
		t.Fatalf("the original error must survive so the failure names the real cause; got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fail-fast took %s — the retry loop is engaging on a real error", elapsed)
	}
}

// TestSettleRetryGivesUpOnAPersistentTransient is the other direction: a transient
// that never clears is a real failure too, and must not spin forever or pass.
func TestSettleRetryGivesUpOnAPersistentTransient(t *testing.T) {
	attempts := 0
	err := settleRetry(func() error {
		attempts++
		return nats.ErrNoStreamResponse
	}, 150*time.Millisecond, 10*time.Millisecond)

	if err == nil {
		t.Fatal("a transient that never clears must fail; returning nil would make the helper a flake-hider")
	}
	if !errors.Is(err, nats.ErrNoStreamResponse) {
		t.Fatalf("the underlying transient must remain inspectable; got %v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected several attempts before giving up; got %d", attempts)
	}
}

// TestSettleRetrySucceedsOnceTheGroupSettles is the happy path the helper exists
// for — the shape of the real failure, where the first call sees the window and a
// later one does not.
func TestSettleRetrySucceedsOnceTheGroupSettles(t *testing.T) {
	attempts := 0
	err := settleRetry(func() error {
		attempts++
		if attempts < 3 {
			return nats.ErrNoStreamResponse
		}
		return nil
	}, 5*time.Second, time.Millisecond)

	if err != nil {
		t.Fatalf("a transient that clears must succeed; got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}
