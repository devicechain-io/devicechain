// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func TestIsRefusal(t *testing.T) {
	for _, err := range []error{ErrStreamBackpressure, ErrUnplacedTenant} {
		require.True(t, IsRefusal(err))
		require.True(t, IsRefusal(fmt.Errorf("wrapped: %w", err)))
	}
	require.False(t, IsRefusal(errors.New("plain")))
	require.False(t, IsRefusal(nil))
}

func TestAdmitPublishTenantAndAnyGroup(t *testing.T) {
	m := n1Manager(t, true)
	_, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	setShardParkGate(t, m, streams.InboundEvents, true)
	setShardParkGate(t, m, GroupSuffix(streams.InboundEvents, 1), false)
	require.NoError(t, m.AdmitPublish("tenant-b", streams.InboundEvents))
	require.ErrorIs(t, m.AdmitPublish("tenant-a", streams.InboundEvents), ErrStreamBackpressure)
	require.NoError(t, m.AdmitPublish("", streams.InboundEvents))
	require.ErrorIs(t, m.AdmitPublish("missing", streams.InboundEvents), ErrUnplacedTenant)
	require.ErrorIs(t, m.AdmitPublish("too-high", streams.InboundEvents), ErrUnplacedTenant)
	setShardParkGate(t, m, GroupSuffix(streams.InboundEvents, 1), true)
	var refusal *BackpressureError
	require.ErrorAs(t, m.AdmitPublish("", streams.InboundEvents), &refusal)
	require.Equal(t, StreamName(m.Microservice.InstanceId, streams.InboundEvents), refusal.Stream)
	// Reverse the accepting group to guard against looking only at the final group.
	setShardParkGate(t, m, streams.InboundEvents, false)
	require.NoError(t, m.AdmitPublish("", streams.InboundEvents))
}

func TestAdmitPublishTenantlessRequiresReadyRouting(t *testing.T) {
	m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
	router := &placementParkRouter{reason: errors.New("no placement")}
	require.NoError(t, m.EnableStreamSharding(router, n1Config(streams.InboundEvents)))
	_, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	setShardParkGate(t, m, streams.InboundEvents, false)
	setShardParkGate(t, m, GroupSuffix(streams.InboundEvents, 1), false)
	require.ErrorIs(t, m.AdmitPublish("", streams.InboundEvents), ErrUnplacedTenant)
	require.ErrorIs(t, m.AdmitPublish("", streams.InboundEvents), router.reason)
}

func TestAdmitPublishBaseGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			m := newBPService(t, startEmbeddedServer(t), "event-sources", bpBounds{}).nmgr
			router := &placementParkRouter{reason: errors.New("must not consult")}
			if enabled {
				require.NoError(t, m.EnableStreamSharding(router, n1Config(streams.ResolvedEvents)))
			}
			_, err := m.NewWriter(streams.InboundEvents)
			require.NoError(t, err)
			for _, closed := range []bool{true, false} {
				setShardParkGate(t, m, streams.InboundEvents, closed)
				for _, tenant := range []string{"", "tenant-b"} {
					err := m.AdmitPublish(tenant, streams.InboundEvents)
					if closed {
						require.ErrorIs(t, err, ErrStreamBackpressure)
					} else {
						require.NoError(t, err)
					}
				}
			}
			require.Zero(t, router.calls.Load())
		})
	}
}
