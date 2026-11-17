// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package shardgroups

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type placementEntry struct {
	value []byte
	op    nats.KeyValueOp
}

func (e placementEntry) Bucket() string             { return "placement" }
func (e placementEntry) Key() string                { return partition.TableKey }
func (e placementEntry) Value() []byte              { return e.value }
func (e placementEntry) Revision() uint64           { return 1 }
func (e placementEntry) Created() time.Time         { return time.Time{} }
func (e placementEntry) Delta() uint64              { return 0 }
func (e placementEntry) Operation() nats.KeyValueOp { return e.op }

func TestPlacementReadiness(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "placement_unavailable"})
	r := &placementStreamRouter{router: partition.NewRouter("test"), count: 2, err: partition.ErrNoPlacement, unavailable: gauge}
	r.setUnavailable()
	require.ErrorIs(t, r.Ready(), partition.ErrNoPlacement)
	require.Equal(t, 1.0, testutil.ToFloat64(gauge))
	put := func(generation uint64, groups ...int) {
		raw, err := partition.Encode(partition.Table{P: 2, Groups: groups, Generation: generation})
		require.NoError(t, err)
		r.update(placementEntry{value: raw, op: nats.KeyValuePut})
	}
	put(1, 0, 1)
	require.NoError(t, r.Ready(), "count-1 is an adoptable group index")
	require.Zero(t, testutil.ToFloat64(gauge))
	put(2, 0, 2)
	require.ErrorIs(t, r.Ready(), ErrGroupsExceedCount)
	require.ErrorContains(t, r.Ready(), "vpartition 1 has group 2")
	require.Equal(t, 1.0, testutil.ToFloat64(gauge))
	put(1, 0, 1)
	require.ErrorIs(t, r.Ready(), ErrGroupsExceedCount, "stale updates must not clear unready")
	put(3, 1, 0)
	require.NoError(t, r.Ready())
	require.Zero(t, testutil.ToFloat64(gauge))
	put(1, 0, 1)
	require.NoError(t, r.Ready(), "stale updates must not flip ready")
	for _, op := range []nats.KeyValueOp{nats.KeyValueDelete, nats.KeyValuePurge} {
		r.update(placementEntry{op: op})
		require.ErrorIs(t, r.Ready(), partition.ErrNoPlacement)
		require.Equal(t, 1.0, testutil.ToFloat64(gauge))
		put(3, 1, 0)
		require.NoError(t, r.Ready())
	}
}
