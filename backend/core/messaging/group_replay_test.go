// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func drainGroupReplay(t *testing.T, r ReplayReader, group int, first, last uint64, hole uint64) {
	t.Helper()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for seq := first; seq <= last; seq++ {
		if seq == hole {
			continue
		}
		msg, err := r.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, seq, msg.StreamSeq)
		require.Equal(t, fmt.Sprintf("g%d/%d", group, seq), string(msg.Value))
		require.Equal(t, ScopedSubject("test", "tenant", GroupSuffix(streams.ResolvedEvents, group)), msg.Subject)
	}
	_, err := r.Read(ctx)
	require.ErrorIs(t, err, io.EOF)
}

func TestGroupReplaySourcePhysicalStreams(t *testing.T) {
	m := n1Manager(t, true)
	_, err := m.ensureStream(streams.ResolvedEvents)
	require.NoError(t, err)
	start := time.Now().Add(-time.Second)
	for group := 0; group < 2; group++ {
		physical := GroupSuffix(streams.ResolvedEvents, group)
		for seq := 1; seq <= 270; seq++ {
			_, err := m.js.Publish(ScopedSubject("test", "tenant", physical), []byte(fmt.Sprintf("g%d/%d", group, seq)))
			require.NoError(t, err)
		}
		require.NoError(t, m.js.DeleteMsg(StreamName("test", physical), 2))
	}
	// Enforce catalog authorization against the base, even for physical g1.
	m.Microservice.Deployed = true
	m.Microservice.FunctionalArea = "event-processing"
	for group := 0; group < 2; group++ {
		source, err := m.NewGroupReplaySource(streams.ResolvedEvents, group)
		require.NoError(t, err)
		r, head, err := source.NewReplayReader(1)
		require.NoError(t, err)
		require.EqualValues(t, 270, head)
		drainGroupReplay(t, r, group, 1, 270, 2)
		r, earliest, err := source.NewReplayReaderFromTime(start)
		require.NoError(t, err)
		require.True(t, earliest.After(start))
		drainGroupReplay(t, r, group, 1, 270, 2)
		third, err := m.js.GetMsg(StreamName("test", GroupSuffix(streams.ResolvedEvents, group)), 3)
		require.NoError(t, err)
		r, _, err = source.NewReplayReaderFromTime(third.Time)
		require.NoError(t, err)
		drainGroupReplay(t, r, group, 3, 270, 0)
		for _, last := range []uint64{4, 270} {
			r, err := source.NewRangeReader(1, last)
			require.NoError(t, err)
			drainGroupReplay(t, r, group, 1, last, 2)
			require.Equal(t, RangeStats{Present: last - 1, Absent: 1}, r.(RangeStatser).RangeStats())
		}
	}
}

func TestGroupReplaySourceValidation(t *testing.T) {
	m := n1Manager(t, true)
	for _, tc := range []struct {
		suffix string
		group  int
	}{
		{streams.FailedDecode, 0}, {streams.ResolvedEvents, -1}, {streams.ResolvedEvents, 2},
		{GroupSuffix(streams.ResolvedEvents, 1), 0},
	} {
		_, err := m.NewGroupReplaySource(tc.suffix, tc.group)
		require.Error(t, err)
	}
	disabled := n1Manager(t, false)
	_, err := disabled.NewGroupReplaySource(streams.ResolvedEvents, 0)
	require.Error(t, err)
	m.Microservice.Deployed = true
	m.Microservice.FunctionalArea = "event-sources"
	_, err = m.NewGroupReplaySource(streams.ResolvedEvents, 1)
	require.ErrorContains(t, err, "is not declared")
}

func TestTenantReplayPhysicalStreamsAndRefusals(t *testing.T) {
	m := n1Manager(t, true)
	_, err := m.ensureStream(streams.ResolvedEvents)
	require.NoError(t, err)
	for group := 0; group < 2; group++ {
		_, err := m.js.Publish(ScopedSubject("test", "tenant", GroupSuffix(streams.ResolvedEvents, group)), []byte(fmt.Sprintf("g%d/1", group)))
		require.NoError(t, err)
	}
	for group, tenant := range []string{"tenant-a", "tenant-b"} {
		r, _, err := m.NewTenantReplayReaderFromTime(n1Context(tenant), streams.ResolvedEvents, time.Time{})
		require.NoError(t, err)
		drainGroupReplay(t, r, group, 1, 1, 0)
	}
	_, _, err = m.NewTenantReplayReaderFromTime(context.Background(), streams.ResolvedEvents, time.Time{})
	require.ErrorIs(t, err, core.ErrNoTenant)
	for _, tenant := range []string{"unplaced", "too-high", "negative"} {
		_, _, err := m.NewTenantReplayReaderFromTime(n1Context(tenant), streams.ResolvedEvents, time.Time{})
		require.ErrorIs(t, err, ErrUnplacedTenant)
	}
	// A real router without an adopted mirror must refuse rather than choose g0.
	cold := n1Manager(t, false)
	require.NoError(t, cold.EnableStreamSharding(partition.NewRouter("test"), n1Config(streams.ResolvedEvents)))
	_, _, err = cold.NewTenantReplayReaderFromTime(n1Context("tenant-b"), streams.ResolvedEvents, time.Time{})
	require.ErrorIs(t, err, ErrUnplacedTenant)
	m.Microservice.Deployed = true
	m.Microservice.FunctionalArea = "event-sources"
	_, _, err = m.NewTenantReplayReaderFromTime(n1Context("tenant-b"), streams.ResolvedEvents, time.Time{})
	require.ErrorContains(t, err, "is not declared")
}

func TestTenantReplayUnselectedMatchesLegacy(t *testing.T) {
	m := n1Manager(t, false)
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), n1Config(streams.InboundEvents)))
	_, err := m.ensureStream(streams.ResolvedEvents)
	require.NoError(t, err)
	_, err = m.js.Publish(ScopedSubject("test", "tenant", streams.ResolvedEvents), []byte("g0/1"))
	require.NoError(t, err)
	legacy, earliest, err := m.NewReplayReaderFromTime(streams.ResolvedEvents, time.Time{})
	require.NoError(t, err)
	r, gotEarliest, err := m.NewTenantReplayReaderFromTime(context.Background(), streams.ResolvedEvents, time.Time{})
	require.NoError(t, err)
	require.Equal(t, earliest, gotEarliest)
	drainGroupReplay(t, legacy, 0, 1, 1, 0)
	drainGroupReplay(t, r, 0, 1, 1, 0)
}

// A document that shards the suffix, on a manager that never enabled routing, refuses
// rather than falling back to the base stream (every other group's history missing).
func TestGroupReplayRefusesConfiguredButUnroutedManager(t *testing.T) {
	m := n1Manager(t, false)
	_, err := m.ensureStream(streams.ResolvedEvents)
	require.NoError(t, err)
	_, err = m.js.Publish(ScopedSubject("test", "tenant", streams.ResolvedEvents), []byte("g0/1"))
	require.NoError(t, err)
	m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = n1Config(streams.ResolvedEvents)

	_, _, err = m.NewTenantReplayReaderFromTime(n1Context("tenant"), streams.ResolvedEvents, time.Time{})
	require.ErrorIs(t, err, ErrShardRoutingNotEnabled)
	_, err = m.NewGroupReplaySource(streams.ResolvedEvents, 0)
	require.ErrorIs(t, err, ErrShardRoutingNotEnabled)
}
