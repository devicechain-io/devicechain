// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// A caller with no deadline still gets a bounded snapshot: the context fn reads under carries a
// deadline no later than ReadSnapshotTimeout from now, and a nested snapshot keeps that same
// bound rather than starting a new one. Without the bound the deadline is simply absent.
func TestReadSnapshotIsBoundedForACallerWithNoDeadline(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := NewStore(&rdb.RdbManager{Database: db})
	start := time.Now()
	require.NoError(t, s.ReadSnapshot(context.Background(), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "a snapshot must carry a deadline even when its caller did not")
		require.False(t, deadline.After(start.Add(ReadSnapshotTimeout+time.Second)))
		return s.ReadSnapshot(ctx, func(inner context.Context) error {
			d2, ok := inner.Deadline()
			require.True(t, ok)
			require.Equal(t, deadline, d2)
			return nil
		})
	}))
}

// A snapshot that outlives its bound is ended with the context's error, not left open.
func TestReadSnapshotPastItsBoundFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := NewStore(&rdb.RdbManager{Database: db})
	prev := ReadSnapshotTimeout
	ReadSnapshotTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ReadSnapshotTimeout = prev })
	err = s.ReadSnapshot(context.Background(), func(ctx context.Context) error {
		// Bounded wait: an unbounded snapshot must FAIL this test, not hang it.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("the snapshot outlived its bound")
		}
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
