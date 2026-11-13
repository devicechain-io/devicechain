// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommitPointBounds(t *testing.T) {
	require.Greater(t, CommitPointDeadline, time.Duration(0))
	require.Less(t, CommitPointDeadline, CommitPointIdleBound)
	require.Equal(t, "SET LOCAL idle_in_transaction_session_timeout = '5000ms'", commitPointIdleSQL)
	require.Equal(t, fmt.Sprintf("SET LOCAL idle_in_transaction_session_timeout = '%dms'", CommitPointIdleBound.Milliseconds()), commitPointIdleSQL,
		"the literal must install CommitPointIdleBound")
}

func TestCommitPointSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	statements := 0
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("commit_point_raw", func(*gorm.DB) {
		statements++
	}))
	wantErr := errors.New("decision failed")
	want := ExecutionDecision{Verdict: VerdictRefuse}
	called := false
	got, err := CommitPoint(context.Background(), db, func(ctx context.Context) (ExecutionDecision, error) {
		called = true
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		remaining := time.Until(deadline)
		require.Greater(t, remaining, time.Duration(0))
		require.LessOrEqual(t, remaining, CommitPointDeadline)
		return want, wantErr
	})
	require.True(t, called)
	require.Equal(t, want, got)
	require.Same(t, wantErr, err)
	require.Zero(t, statements, "SQLite must run no Raw/Exec statement")
}

func TestCommitPointDiscardsAnAnswerAfterTheDeadline(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	got, err := CommitPoint(context.Background(), db, func(ctx context.Context) (ExecutionDecision, error) {
		<-ctx.Done()
		return ExecutionDecision{Verdict: VerdictApprove}, nil
	})
	require.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	require.Equal(t, ExecutionDecision{}, got)
}
