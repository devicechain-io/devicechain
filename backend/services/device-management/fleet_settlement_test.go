// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// A failing sweep must not stop the fast pass (it ran first and returned, before), nor
// re-run the full sweep on every five-second tick.
func TestSettlementTickFastPassSurvivesAFailingSweep(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	sweeps, fasts := 0, 0
	boom := errors.New("sweep failed")
	tick := &settlementTick{
		now:   func() time.Time { return now },
		sweep: func(context.Context) error { sweeps++; return boom },
		fast:  func(context.Context) error { fasts++; return nil },
	}
	err := tick.run(context.Background())
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, fasts, "the fast pass runs although the sweep failed")
	for range 5 {
		now = now.Add(5 * time.Second)
		require.NoError(t, tick.run(context.Background()))
	}
	require.Equal(t, 1, sweeps, "a failed sweep is not retried on every tick")
	require.Equal(t, 6, fasts)
	now = now.Add(fleetSettlementSweepRetry)
	require.ErrorIs(t, tick.run(context.Background()), boom)
	require.Equal(t, 2, sweeps, "a failed sweep is retried")

	// A successful (or skipped) sweep waits the full interval.
	tick.sweep = func(context.Context) error { sweeps++; return core.ErrPassSkipped }
	now = now.Add(fleetSettlementSweepRetry)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 3, sweeps)
	now = now.Add(fleetSettlementSweepEvery - time.Second)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 3, sweeps)
	now = now.Add(time.Second)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 4, sweeps)
}
