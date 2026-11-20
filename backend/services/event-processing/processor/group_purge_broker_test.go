// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 🔴 A SHARDED ENGINE ANSWERS A TENANT EVICTION UNDER ITS OWN BROKER GRANT, ON A BROKER THAT
// ENFORCES IT. The responder replies to user-management's inbox with no publish grant on it: the
// broker lets event-processing's login publish there only because that login received the request
// (allow_responses), and it counts that allowance per connection and reply subject, ONE reply
// each. Every partition of a sharded engine lives in the same process, on the same connection, so
// a responder that answers once per partition spends that allowance on its first delivery and has
// every answer refused — and the deletion waits forever on partitions that did evict. The unit
// tests elsewhere in this package run on a permissive broker, where the same code is green.
func TestShardedEvictionIsAnsweredUnderTheGeneratedGrant(t *testing.T) {
	b := permtest.Start(t, evictInstance, permtest.WithoutReconcile())
	engine := b.Connect(t, "event-processing")
	requester := b.Connect(t, "user-management")

	parent := newEvictionRig(t, newTestStore(t), "acme", "globex")
	var children []*evictionRig
	expected := []string{singletonPartition}
	for g := 1; g <= 2; g++ {
		child := newEvictionRig(t, newTestStore(t), "acme", "globex")
		child.rp.worker = &workerIdentity{group: g, partition: groupPartition(g)}
		children = append(children, child)
		parent.rp.groupWorkers = append(parent.rp.groupWorkers, child.rp)
		expected = append(expected, groupPartition(g))
	}
	parent.start(t)
	defer parent.stop()
	for _, child := range children {
		child.start(t)
		defer child.stop()
	}

	responder := NewTenantPurgeResponder(engine, evictInstance, parent.rp)
	require.NoError(t, responder.Start())
	defer func() { _ = responder.Stop() }()

	result, err := messaging.PurgeTenantDetect(context.Background(), requester, evictInstance, "acme", expected)
	require.NoError(t, err)
	b.RequireNoViolations(t)
	answered := map[string]bool{}
	for _, reply := range result.Replies {
		require.Empty(t, reply.Error, "partition %q", reply.PartitionId)
		answered[reply.PartitionId] = true
	}
	for _, id := range expected {
		require.True(t, answered[id], "partition %q evicted but its answer never reached the requester", id)
	}
	require.Len(t, result.Replies, len(expected), "each partition answers exactly once")
}

// stubResponder is a sharded responder whose partitions evict through the supplied functions.
func stubResponder(nc *nats.Conn, parent func(context.Context, string) (int64, error),
	children ...func(context.Context, string) (int64, error)) *TenantPurgeResponder {
	r := &TenantPurgeResponder{conn: nc, instanceId: evictInstance, partition: singletonPartition, evict: parent}
	for i, evict := range children {
		r.children = append(r.children, &TenantPurgeResponder{conn: nc, instanceId: evictInstance,
			partition: groupPartition(i + 1), evict: evict})
	}
	return r
}

// The partitions evict side by side. They answer in one reply, so evicting them one after
// another would add their commits together and could push the reply past the caller's window,
// silencing every partition for the slowest one. Each stub waits until the other has started:
// run in turn, the first would wait out its budget and answer with an error.
func TestTheGroupsOfOneProcessEvictConcurrently(t *testing.T) {
	nc := purgeConn(t, purgeBroker(t))
	var started sync.WaitGroup
	started.Add(2)
	meet := func(ctx context.Context, _ string) (int64, error) {
		started.Done()
		done := make(chan struct{})
		go func() { started.Wait(); close(done) }()
		select {
		case <-done:
			return 1, nil
		case <-ctx.Done():
			return 0, errors.New("the other partition never started while this one was evicting")
		}
	}
	responder := stubResponder(nc, meet, meet)
	require.NoError(t, responder.Start())
	defer func() { _ = responder.Stop() }()

	result, err := messaging.PurgeTenantDetect(context.Background(), nc, evictInstance, "acme",
		[]string{singletonPartition, groupPartition(1)})
	require.NoError(t, err)
	require.Len(t, result.Replies, 2)
	for _, reply := range result.Replies {
		require.Empty(t, reply.Error, "partition %q", reply.PartitionId)
	}
}

// A partition whose eviction does not finish answers with an error INSIDE the caller's window,
// so the reply still carries its siblings' answers. Its budget is what guarantees that: a
// budget as long as the window would deliver the reply after the caller stopped listening.
func TestAWedgedGroupAnswersInsideTheCallersWindow(t *testing.T) {
	nc := purgeConn(t, purgeBroker(t))
	var budget atomic.Int64
	wedged := func(ctx context.Context, _ string) (int64, error) {
		if deadline, ok := ctx.Deadline(); ok {
			budget.Store(int64(time.Until(deadline)))
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	clean := func(context.Context, string) (int64, error) { return 2, nil }
	responder := stubResponder(nc, clean, wedged)
	require.NoError(t, responder.Start())
	defer func() { _ = responder.Stop() }()

	result, err := messaging.PurgeTenantDetect(context.Background(), nc, evictInstance, "acme",
		[]string{singletonPartition, groupPartition(1)})
	require.NoError(t, err)
	got := time.Duration(budget.Load())
	require.Positive(t, got, "the wedged partition was not given a deadline")
	require.LessOrEqual(t, got, messaging.DetectPurgeWindow-500*time.Millisecond,
		"a partition's budget must leave the reply time to reach the caller")
	answers := map[string]string{}
	for _, reply := range result.Replies {
		answers[reply.PartitionId] = reply.Error
	}
	require.Contains(t, answers, singletonPartition, "the clean partition's answer was lost with the wedged one")
	require.Empty(t, answers[singletonPartition])
	require.NotEmpty(t, answers[groupPartition(1)], "a wedged partition must answer as still holding the tenant")
}
