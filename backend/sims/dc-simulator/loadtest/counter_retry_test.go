// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flakyCounter fails its first `failures` reads and then answers n.
type flakyCounter struct {
	failures int
	n        int64
	calls    int
}

func (f *flakyCounter) Count(context.Context, Window) (int64, error) {
	f.calls++
	if f.calls <= f.failures {
		return 0, fmt.Errorf("count events: context deadline exceeded")
	}
	return f.n, nil
}

func noWait(sleeps *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil }
}

// A count that times out and then answers is the answer: the run goes on to its verdict.
func TestRetryingCounterRecoversFromTimeouts(t *testing.T) {
	var sleeps []time.Duration
	inner := &flakyCounter{failures: 3, n: 5_001_178}
	c := &retryingCounter{inner: inner, attempts: 5, backoff: 5 * time.Second, maxBackoff: 12 * time.Second, sleep: noWait(&sleeps)}
	got, err := c.Count(context.Background(), Window{})
	require.NoError(t, err)
	assert.Equal(t, int64(5_001_178), got)
	assert.Equal(t, 4, inner.calls)
	assert.Equal(t, []time.Duration{5 * time.Second, 10 * time.Second, 12 * time.Second}, sleeps,
		"the backoff doubles and is capped")
}

// NEGATIVE CONTROL: a read that never answers is not retried forever and is not a zero. It is an
// error after the bounded attempts, which the caller reports as no verdict.
func TestRetryingCounterGivesUpAfterTheBound(t *testing.T) {
	var sleeps []time.Duration
	inner := &flakyCounter{failures: 1 << 30}
	c := &retryingCounter{inner: inner, attempts: 4, backoff: time.Second, maxBackoff: time.Minute, sleep: noWait(&sleeps)}
	got, err := c.Count(context.Background(), Window{})
	require.Error(t, err)
	assert.Zero(t, got)
	assert.Contains(t, err.Error(), "failed 4 times")
	assert.Equal(t, 4, inner.calls)
	assert.Len(t, sleeps, 3, "no sleep after the last attempt")
}

// A cancelled run stops at once instead of backing off through its remaining attempts.
func TestRetryingCounterStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := &flakyCounter{failures: 1 << 30}
	c := &retryingCounter{inner: inner, attempts: 5, backoff: time.Hour, maxBackoff: time.Hour}
	_, err := c.Count(ctx, Window{})
	require.Error(t, err)
	assert.Equal(t, 1, inner.calls)
}
