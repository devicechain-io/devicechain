// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// withFastRetry shrinks the retry budget so tests do not wait the production
// cadence, restoring it afterwards.
func withFastRetry(attempts int) func() {
	pa, pd := infraConnectAttempts, infraConnectDelay
	infraConnectAttempts, infraConnectDelay = attempts, time.Millisecond
	return func() { infraConnectAttempts, infraConnectDelay = pa, pd }
}

// A connect that fails a few times then succeeds returns nil once it succeeds.
func TestRetryInfraConnectSucceedsAfterFailures(t *testing.T) {
	defer withFastRetry(5)()
	calls := 0
	err := RetryInfraConnect(context.Background(), "test", func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not ready")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

// A connect that never succeeds returns the last error after the attempt budget.
func TestRetryInfraConnectExhausts(t *testing.T) {
	defer withFastRetry(3)()
	calls := 0
	sentinel := errors.New("down")
	err := RetryInfraConnect(context.Background(), "test", func(context.Context) error {
		calls++
		return sentinel
	})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

// A cancelled context returns promptly without exhausting the attempt budget.
func TestRetryInfraConnectContextCancel(t *testing.T) {
	defer withFastRetry(1000)()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RetryInfraConnect(ctx, "test", func(context.Context) error {
		calls++
		cancel() // cancel mid-flight; the retry must not keep looping
		return errors.New("not ready")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected to stop after 1 attempt, got %d", calls)
	}
}
