// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// referenceDecoderHealth is the tally PayloadDecoderHealth computed before the sums moved into SQL:
// every row in the window, in (window_start, id) order, summed in Go, with the last error taken from
// the row whose failure is strictly latest, so among equal failure times the earliest row wins.
func referenceDecoderHealth(rows []ProfileDecoderHealth) DecoderHealth {
	var out DecoderHealth
	out.Windows = len(rows)
	for _, r := range rows {
		out.Decoded += r.Decoded
		out.Ignored += r.Ignored
		out.Failed += r.Failed
		out.Suppressed += r.Suppressed
		out.ChargeSkew += r.ChargeSkew
		if r.LastFailureAt != nil && (out.LastFailureAt == nil || r.LastFailureAt.After(*out.LastFailureAt)) {
			t := *r.LastFailureAt
			out.LastFailureAt, out.LastError = &t, r.LastError
		}
	}
	return out
}

// 🔴 THE SQL TALLY IS THE GO TALLY. Many random spreads of rows over replicas, versions and
// minutes, inside and outside the window, with failure times that tie on purpose (the last-error
// rule is decided by the tie), each read back through PayloadDecoderHealth and compared with the
// reference above over the same rows read the old way.
func TestTheSQLDecoderHealthTallyEqualsTheGoTally(t *testing.T) {
	checkSQLDecoderHealthTally(t, 25, func() (*Api, context.Context) {
		f := newHealthFixture(t)
		return f.api, f.ctx
	})
}

// checkSQLDecoderHealthTally runs the comparison for `rounds` rounds, each on a fresh profile from
// fresh(), which returns the API and a tenant context holding a profile "meter". It is shared with
// the Postgres integration test: the last-error rule turns on how the server orders NULLs, which
// SQLite and Postgres do differently.
func checkSQLDecoderHealthTally(t *testing.T, rounds int, fresh func() (*Api, context.Context)) {
	t.Helper()
	rng := rand.New(rand.NewSource(2269))
	for round := 0; round < rounds; round++ {
		api, ctx := fresh()
		f := struct {
			api *Api
			ctx context.Context
		}{api, ctx}
		now := time.Now().UTC().Truncate(time.Minute)
		base := now.Add(-30 * time.Minute)
		ties := []time.Time{base.Add(3 * time.Second), base.Add(7 * time.Second)}
		var writes []DecoderHealthWrite
		n := 1 + rng.Intn(30)
		for i := 0; i < n; i++ {
			w := DecoderHealthWrite{
				ProfileToken: "meter",
				Version:      int32(1 + rng.Intn(2)),
				// Some rows sit before the 60-minute window and must not count.
				WindowStart: now.Add(-time.Duration(rng.Intn(80)) * time.Minute),
				// Few replica names, so replicas repeat across minutes and versions (and an upsert may
				// merge two writes into one row): the window count is rows, not replicas.
				Replica: fmt.Sprintf("pod-%d", rng.Intn(3)),
				Decoded: rng.Int63n(1000), Ignored: rng.Int63n(50), Failed: rng.Int63n(20),
				Suppressed: rng.Int63n(5), ChargeSkew: rng.Int63n(3),
			}
			switch rng.Intn(3) {
			case 0: // no failure
			case 1: // a failure time shared with other rows: the tie decides the last error
				ft := ties[rng.Intn(len(ties))]
				w.LastFailureAt, w.LastError = &ft, fmt.Sprintf("tie-%d", i)
			default:
				ft := base.Add(time.Duration(rng.Intn(3000)) * time.Millisecond)
				w.LastFailureAt, w.LastError = &ft, fmt.Sprintf("err-%d", i)
			}
			writes = append(writes, w)
		}
		_, _, err := f.api.WriteDecoderHealth(f.ctx, writes)
		require.NoError(t, err)

		got, err := f.api.PayloadDecoderHealth(writerCtx(f.ctx), "meter", 60)
		require.NoError(t, err)

		var rows []ProfileDecoderHealth
		require.NoError(t, f.api.RDB.DB(f.ctx).
			Where("window_start >= ?", got.Since).
			Order("window_start ASC, id ASC").Find(&rows).Error)
		want := referenceDecoderHealth(rows)

		require.Equal(t, want.Windows, got.Windows, "round %d windows", round)
		require.Equal(t, want.Decoded, got.Decoded, "round %d decoded", round)
		require.Equal(t, want.Ignored, got.Ignored, "round %d ignored", round)
		require.Equal(t, want.Failed, got.Failed, "round %d failed", round)
		require.Equal(t, want.Suppressed, got.Suppressed, "round %d suppressed", round)
		require.Equal(t, want.ChargeSkew, got.ChargeSkew, "round %d charge skew", round)
		require.Equal(t, want.LastError, got.LastError, "round %d last error", round)
		if want.LastFailureAt == nil {
			require.Nil(t, got.LastFailureAt, "round %d", round)
		} else {
			require.NotNil(t, got.LastFailureAt, "round %d", round)
			require.True(t, want.LastFailureAt.Equal(*got.LastFailureAt), "round %d last failure %v vs %v", round, want.LastFailureAt, got.LastFailureAt)
		}
	}
}

// An empty window reads as zero windows and no failure, not as an error.
func TestAnEmptyDecoderHealthWindowIsZero(t *testing.T) {
	f := newHealthFixture(t)
	got, err := f.api.PayloadDecoderHealth(writerCtx(f.ctx), "meter", 60)
	require.NoError(t, err)
	require.Zero(t, got.Windows)
	require.Zero(t, got.Decoded)
	require.Nil(t, got.LastFailureAt)
	require.Empty(t, got.LastError)
}
