// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

var _ ShardGroupRouter = (*partition.Router)(nil)

type shardRouterFunc func(string) (int, error)

func (f shardRouterFunc) ShardGroup(tenant string) (int, error) { return f(tenant) }

func n1Router(tenant string) (int, error) {
	switch tenant {
	case "tenant-a":
		return 0, nil
	case "tenant-b":
		return 1, nil
	case "too-high":
		return 5, nil
	case "negative":
		return -1, nil
	default:
		return 0, errors.New("no placement loaded")
	}
}

func n1Config(suffixes ...string) *config.NatsShardGroupsConfiguration {
	return &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: suffixes}
}

func n1Manager(t *testing.T, enabled bool) *NatsManager {
	t.Helper()
	m, _ := metricsManager(t, startEmbeddedServer(t))
	if enabled {
		require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents, streams.ResolvedEvents)))
	}
	return m
}

func n1Context(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }

func n1Stored(t *testing.T, m *NatsManager, suffix string, count uint64) *nats.StreamInfo {
	t.Helper()
	info, err := m.js.StreamInfo(StreamName(m.Microservice.InstanceId, suffix))
	require.NoError(t, err)
	require.Equal(t, count, info.State.Msgs, "stream %s", suffix)
	return info
}

func n1Read(t *testing.T, r MessageReader, subject, value string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, err := r.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, subject, m.Subject)
	require.Equal(t, value, string(m.Value))
	require.NoError(t, m.Ack())
}

func TestStreamShardingDisabledPreservesTrafficAndStreamList(t *testing.T) {
	m := n1Manager(t, false) // EnableStreamSharding is never called.
	w, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	r, err := m.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	nr := r.(*natsReader)
	require.Equal(t, "test_inbound-events", nr.stream)
	require.Equal(t, "test.*.inbound-events", nr.subject)
	require.Equal(t, ReaderDurableName("test", m.Microservice.FunctionalArea, streams.InboundEvents), nr.durable)
	require.NoError(t, w.WriteMessages(n1Context("tenant-a"), Message{Value: []byte("legacy")}))
	raw, err := m.js.GetMsg("test_inbound-events", 1)
	require.NoError(t, err)
	require.Equal(t, "test.tenant-a.inbound-events", raw.Subject)
	n1Read(t, r, "test.tenant-a.inbound-events", "legacy")
	var names []string
	for name := range m.js.StreamNames() {
		names = append(names, name)
	}
	sort.Strings(names)
	require.Equal(t, []string{"test_inbound-events"}, names)
}

func TestStreamShardingRoutesTenantsAndBindsEveryGroup(t *testing.T) {
	m := n1Manager(t, true)
	w, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		require.NoError(t, w.WriteMessages(n1Context(tenant), Message{Value: []byte(tenant)}))
	}
	base := n1Stored(t, m, streams.InboundEvents, 1)
	group := n1Stored(t, m, "inbound-events-g1", 1)
	require.Equal(t, []string{"test.*.inbound-events-g1"}, group.Config.Subjects)
	require.Equal(t, base.Config.MaxBytes, group.Config.MaxBytes)
	require.Equal(t, base.Config.MaxMsgs, group.Config.MaxMsgs)
	require.Equal(t, base.Config.MaxMsgSize, group.Config.MaxMsgSize)
	require.Equal(t, base.Config.MaxAge, group.Config.MaxAge)
	require.Equal(t, base.Config.Retention, group.Config.Retention)
	require.Equal(t, base.Config.Duplicates, group.Config.Duplicates)
	require.Equal(t, base.Config.Replicas, group.Config.Replicas)
	require.NotZero(t, group.Config.MaxBytes)
	for g, tenant := range []string{"tenant-a", "tenant-b"} {
		suffix := GroupSuffix(streams.InboundEvents, g)
		raw, err := m.js.GetMsg(StreamName("test", suffix), 1)
		require.NoError(t, err)
		require.Equal(t, ScopedSubject("test", tenant, suffix), raw.Subject)
		r, err := m.NewReader(streams.InboundEvents, ReaderWithShardGroup(g))
		require.NoError(t, err)
		nr := r.(*natsReader)
		require.Equal(t, StreamName("test", suffix), nr.stream)
		require.Equal(t, StreamSubject("test", suffix), nr.subject)
		require.Equal(t, ReaderDurableName("test", m.Microservice.FunctionalArea, suffix), nr.durable)
		n1Read(t, r, ScopedSubject("test", tenant, suffix), tenant)
		// No other tenant can be delivered by this group reader.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err = r.ReadMessage(ctx)
		cancel()
		require.Error(t, err)
	}
}

func TestStreamShardingUnplacedPublishesStoreNothing(t *testing.T) {
	for _, tenant := range []string{"unplaced", "too-high", "negative"} {
		t.Run(tenant, func(t *testing.T) {
			m := n1Manager(t, true)
			w, err := m.NewWriter(streams.InboundEvents)
			require.NoError(t, err)
			err = w.WriteMessages(n1Context(tenant), Message{Value: []byte("must not store")})
			require.ErrorIs(t, err, ErrUnplacedTenant)
			n1Stored(t, m, streams.InboundEvents, 0)
			n1Stored(t, m, "inbound-events-g1", 0)
		})
	}
}

func TestStreamShardingReaderRefusals(t *testing.T) {
	m := n1Manager(t, false)
	_, err := m.NewReader(streams.InboundEvents, ReaderWithShardGroup(0))
	require.ErrorContains(t, err, "disabled")
	m = n1Manager(t, false) // A handle attempt freezes routing, even when refused.
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
	_, err = m.NewReader(streams.InboundEvents)
	require.ErrorContains(t, err, "needs one reader per group")
	for _, g := range []int{-1, 2, 5} {
		_, err = m.NewReader(streams.InboundEvents, ReaderWithShardGroup(g))
		require.ErrorContains(t, err, "out of range")
	}
	_, err = m.NewReader(streams.ResolvedEvents, ReaderWithShardGroup(1))
	require.ErrorContains(t, err, "not sharded")
	_, err = m.NewReader(streams.ResolvedEvents, ReaderWithShardGroup(0))
	require.NoError(t, err)
	_, err = m.NewReader(streams.ResolvedEvents)
	require.NoError(t, err)
}

func TestEnableStreamShardingRefusesInvalidInputsAndCopiesConfig(t *testing.T) {
	m := &NatsManager{}
	cfg := n1Config(streams.InboundEvents)
	require.ErrorContains(t, m.EnableStreamSharding(nil, cfg), "nil")
	var typedNil shardRouterFunc
	require.ErrorContains(t, m.EnableStreamSharding(typedNil, cfg), "nil")
	require.ErrorContains(t, m.EnableStreamSharding(shardRouterFunc(n1Router), nil), "nil")
	require.Error(t, m.EnableStreamSharding(shardRouterFunc(n1Router), &config.NatsShardGroupsConfiguration{Count: 1}))
	require.Error(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config("unknown")))
	cfg.PlacementTags = map[int][]string{1: {"rack-a"}}
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
	cfg.Count = 3
	cfg.Suffixes[0] = streams.ResolvedEvents
	cfg.PlacementTags[1][0] = "mutated"
	suffix, err := m.tenantSuffix("tenant-b", streams.InboundEvents)
	require.NoError(t, err)
	require.Equal(t, "inbound-events-g1", suffix)
	require.Equal(t, []string{"rack-a"}, m.groupPlacement(suffix).Tags)
	require.Equal(t, 2, m.streamSharding().cfg.Count)
	require.ErrorContains(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)), "already enabled")
	suffix, err = m.tenantSuffix("unplaced", streams.ResolvedEvents)
	require.NoError(t, err, "an unselected suffix must not consult placement")
	require.Equal(t, streams.ResolvedEvents, suffix)
}

func TestStreamShardingPlacementInComputedCreateConfig(t *testing.T) {
	m := n1Manager(t, false)
	cfg := n1Config(streams.InboundEvents)
	cfg.PlacementTags = map[int][]string{1: {"rack-a", "ssd"}}
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
	// This is the StreamConfig ensureSingleStream sends to AddStream. A standalone
	// server cannot satisfy placement tags, so inspect the create configuration.
	base := m.newStreamConfig(streams.InboundEvents)
	group := m.newStreamConfig("inbound-events-g1")
	require.Nil(t, base.Placement)
	require.Equal(t, &nats.Placement{Tags: []string{"rack-a", "ssd"}}, group.Placement)
	require.Equal(t, "test_inbound-events-g1", group.Name)
	require.Equal(t, []string{"test.*.inbound-events-g1"}, group.Subjects)
	group.Placement.Tags[0] = "changed"
	require.Equal(t, "rack-a", m.newStreamConfig("inbound-events-g1").Placement.Tags[0])
}

func TestGroupSuffixPreservesZeroAndRejectsNegative(t *testing.T) {
	require.Equal(t, streams.InboundEvents, GroupSuffix(streams.InboundEvents, 0))
	require.Equal(t, "inbound-events-g12", GroupSuffix(streams.InboundEvents, 12))
	require.Panics(t, func() { GroupSuffix(streams.InboundEvents, -1) })
}

func TestStreamShardingAllWriterPaths(t *testing.T) {
	for _, path := range []string{"sync", "ordered", "async", "round"} {
		t.Run(path, func(t *testing.T) {
			m := n1Manager(t, true)
			var publish func(context.Context, Message) error
			await := func(submit func(func(error))) error {
				done := make(chan error, 1)
				submit(func(err error) { done <- err })
				select {
				case err := <-done:
					return err
				case <-time.After(5 * time.Second):
					return fmt.Errorf("publish callback timed out")
				}
			}
			switch path {
			case "sync":
				w, err := m.NewWriter(streams.ResolvedEvents)
				require.NoError(t, err)
				publish = func(ctx context.Context, msg Message) error { return w.WriteMessages(ctx, msg) }
			case "ordered":
				w, err := m.NewOrderedWriter(streams.ResolvedEvents, WriterResolution, 4)
				require.NoError(t, err)
				defer w.Close()
				publish = func(ctx context.Context, msg Message) error {
					return await(func(done func(error)) { w.Publish(ctx, msg, done) })
				}
			case "async":
				w, err := m.NewAsyncWriter(streams.ResolvedEvents, WriterResolution, 4, false)
				require.NoError(t, err)
				defer w.Close()
				publish = func(ctx context.Context, msg Message) error {
					return await(func(done func(error)) { w.Submit(ctx, []Message{msg}, done) })
				}
			case "round":
				w, err := m.NewRoundPublisher(streams.ResolvedEvents)
				require.NoError(t, err)
				publish = func(ctx context.Context, msg Message) error { _, err := w.Publish(ctx, msg); return err }
			}
			for _, tenant := range []string{"tenant-a", "tenant-b"} {
				require.NoError(t, publish(n1Context(tenant), Message{Value: []byte(tenant)}))
			}
			for _, tenant := range []string{"unplaced", "too-high", "negative"} {
				require.ErrorIs(t, publish(n1Context(tenant), Message{Value: []byte("refuse")}), ErrUnplacedTenant)
			}
			n1Stored(t, m, streams.ResolvedEvents, 1)
			n1Stored(t, m, "resolved-events-g1", 1)
			// Round read-back must follow placement too; both streams have sequence 1.
			rounds, err := m.NewRoundPublisher(streams.ResolvedEvents)
			require.NoError(t, err)
			stored, err := rounds.Get(context.Background(), "tenant-b", 1)
			require.NoError(t, err)
			require.Equal(t, "test.tenant-b.resolved-events-g1", stored.Subject)
			require.Equal(t, "tenant-b", string(stored.Value))
			_, err = rounds.Get(context.Background(), "unplaced", 1)
			require.ErrorIs(t, err, ErrUnplacedTenant)
		})
	}
}

func TestStreamShardingEnvelopesStayWithinOneTenantGroup(t *testing.T) {
	m := n1Manager(t, true)
	w, err := m.NewRoundPublisher(streams.InboundEvents)
	require.NoError(t, err)
	f := newTestFormer(t, FormerConfig{MaxMembers: 2})
	now := time.Unix(1000, 0)
	member := func(tenant string) FormedMember {
		return FormedMember{Bytes: pb(tenant), Token: tenant}
	}
	// Teach both tenants' predictive lingers a busy rate, then start clean batches.
	for i := 0; i < 10; i++ {
		f.Add(now, "tenant-a", member("tenant-a"))
		f.Add(now, "tenant-b", member("tenant-b"))
		now = now.Add(time.Millisecond)
		f.Expired(now)
	}
	f.Drain(now)
	var batches []FormedBatch
	for i := 0; i < 2; i++ {
		for _, tenant := range []string{"tenant-a", "tenant-b"} {
			batches = append(batches, f.Add(now, tenant, member(tenant))...)
		}
	}
	require.Len(t, batches, 2)
	for _, batch := range batches {
		require.Len(t, batch.Members, 2)
		var events []eventenvelope.Event
		for i, member := range batch.Members {
			require.Equal(t, batch.Tenant, member.Token, "an envelope cannot mix tenants or groups")
			events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: member.Bytes})
		}
		encoded, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: batch.Tenant}, events)
		require.NoError(t, err)
		_, err = w.Publish(n1Context(batch.Tenant), Message{Value: encoded, Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}})
		require.NoError(t, err)
	}
	for g, tenant := range []string{"tenant-a", "tenant-b"} {
		suffix := GroupSuffix(streams.InboundEvents, g)
		n1Stored(t, m, suffix, 1)
		stored, err := w.Get(context.Background(), tenant, 1)
		require.NoError(t, err)
		require.Equal(t, ScopedSubject("test", tenant, suffix), stored.Subject)
		require.Equal(t, eventenvelope.HeaderValue, stored.Headers[eventenvelope.HeaderName])
		envelope, err := eventenvelope.Parse(stored.Value)
		require.NoError(t, err)
		require.NoError(t, envelope.VerifyTenant(tenant))
		require.Equal(t, 2, envelope.Len())
		for i := 0; i < envelope.Len(); i++ {
			value, err := envelope.Member(i)
			require.NoError(t, err)
			require.Equal(t, pb(tenant), value)
		}
	}
}

// A live feed on a sharded suffix listens on the tenant's group subject, so a group-1
// tenant's live subscriber sees its own messages; an unplaced tenant is refused instead
// of being handed a silent feed on group 0's subject.
func TestStreamShardingLiveFeedFollowsPlacement(t *testing.T) {
	m := n1Manager(t, true)
	w, err := m.NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	live, err := m.SubscribeLive(ctx, "tenant-b", streams.ResolvedEvents)
	require.NoError(t, err)
	require.NoError(t, w.WriteMessages(n1Context("tenant-b"), Message{Value: []byte("live-b")}))
	select {
	case got := <-live:
		require.Equal(t, ScopedSubject("test", "tenant-b", GroupSuffix(streams.ResolvedEvents, 1)), got.Subject)
		require.Equal(t, "live-b", string(got.Value))
	case <-ctx.Done():
		t.Fatal("a group-1 tenant's live feed received nothing")
	}
	_, err = m.SubscribeLive(ctx, "unplaced", streams.ResolvedEvents)
	require.ErrorIs(t, err, ErrUnplacedTenant)
}
