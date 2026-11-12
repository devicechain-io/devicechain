// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// Minimal test-only copy of jsGroupNotServingYet and settleRetry from
// backend/core/messaging/replica_lift_cluster_test.go. Keep the classification in
// sync: only a group's election/route propagation window may be retried.
func jsGroupNotServingYet(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, nats.ErrNoStreamResponse) || errors.Is(err, jetstream.ErrNoStreamResponse) ||
		errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no response from stream") ||
		strings.Contains(msg, "no responders available")
}

// settleRetry also bounds each API request by the retry deadline. It must never
// wrap a publish, fetch, ack, or the loss/duplication oracle.
func settleRetry(op func(context.Context) error, deadline time.Time, gap time.Duration) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for attempts := 1; ; attempts++ {
		err := op(ctx)
		if err == nil {
			return nil
		}
		if !jsGroupNotServingYet(err) {
			return err
		}
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("group never began serving after %d attempts: %w", attempts, err)
		case <-timer.C:
		}
	}
}

func retryWhileGroupSettles(t testing.TB, what string, deadline time.Time, op func(context.Context) error) {
	t.Helper()
	// Polling the kill/recovery state retains its original deadline, with at most
	// five seconds of settling tolerance per metadata call.
	deadline = minDeadline(deadline, time.Now().Add(5*time.Second))
	require.NoError(t, settleRetry(op, deadline, 50*time.Millisecond), what)
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func consumerInfoWhileSettling(t testing.TB, js nats.JetStreamContext, stream, durable string, deadline time.Time) *nats.ConsumerInfo {
	t.Helper()
	var info *nats.ConsumerInfo
	retryWhileGroupSettles(t, "consumer info "+durable, deadline, func(ctx context.Context) error {
		var err error
		info, err = js.ConsumerInfo(stream, durable, nats.Context(ctx))
		return err
	})
	return info
}

// Override only ConsumerInfo; an accidental call to another API panics.
type settlingConsumerInfo struct {
	nats.JetStreamContext
	call func() (*nats.ConsumerInfo, error)
}

func (js settlingConsumerInfo) ConsumerInfo(_, _ string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	return js.call()
}

func TestEnvelopeConsumerInfoRetriesFirstNoResponders(t *testing.T) {
	want := &nats.ConsumerInfo{Name: "durable"}
	calls := 0
	js := settlingConsumerInfo{call: func() (*nats.ConsumerInfo, error) {
		calls++
		if calls == 1 {
			return nil, nats.ErrNoResponders
		}
		return want, nil
	}}
	require.Same(t, want, consumerInfoWhileSettling(t, js, "inbound", "durable", time.Now().Add(time.Second)))
	require.Equal(t, 2, calls)
}

func TestEnvelopeConsumerReadinessWaitsForLeader(t *testing.T) {
	calls := 0
	js := settlingConsumerInfo{call: func() (*nats.ConsumerInfo, error) {
		calls++
		if calls == 1 {
			return nil, nats.ErrNoResponders
		}
		if calls == 2 {
			return &nats.ConsumerInfo{}, nil
		}
		return &nats.ConsumerInfo{Cluster: &nats.ClusterInfo{Leader: "leader"}}, nil
	}}
	benchConsumerSettled(t, js, "inbound", "durable")
	require.Equal(t, 3, calls, "a successful ConsumerInfo without a leader is not ready")
}

func TestEnvelopeSettleRetryRejectsNonSettlingError(t *testing.T) {
	calls := 0
	err := settleRetry(func(context.Context) error {
		calls++
		return nats.ErrConsumerNotFound
	}, time.Now().Add(50*time.Millisecond), time.Millisecond)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound)
	// A retry-every-error mutant reaches the deadline but violates this assertion.
	require.Equal(t, 1, calls, "non-settling errors must fail on the first attempt")
}

func TestEnvelopeSettleRetryBoundsPersistentSettling(t *testing.T) {
	deadline := time.Now().Add(20 * time.Millisecond)
	err := settleRetry(func(ctx context.Context) error {
		got, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, deadline, got)
		return nats.ErrNoResponders
	}, deadline, time.Millisecond)
	require.ErrorIs(t, err, nats.ErrNoResponders)
	require.Contains(t, err.Error(), "group never began serving")
}

func TestEnvelopeGroupNotServingYet(t *testing.T) {
	for _, err := range []error{
		nats.ErrNoResponders, nats.ErrNoStreamResponse, jetstream.ErrNoStreamResponse,
		nats.ErrTimeout, context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", nats.ErrNoResponders),
		errors.New("no response from stream"), errors.New("no responders available for request"),
	} {
		require.True(t, jsGroupNotServingYet(err), "%v", err)
	}
	for _, err := range []error{nil, nats.ErrConsumerNotFound, errors.New("permission denied")} {
		require.False(t, jsGroupNotServingYet(err), "%v", err)
	}
}
