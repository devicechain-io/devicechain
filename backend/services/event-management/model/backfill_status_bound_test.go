// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListBackfillsStatusBound(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&CalculatedBackfill{}))
	api := &Api{RDB: &rdb.RdbManager{Database: db}}
	ctx := startCtx()
	statuses := []string{BackfillQueued, BackfillRunning, BackfillSucceeded, BackfillCancelled, BackfillFailed}
	var tokens []string
	for i, status := range statuses {
		token := fmt.Sprintf("job-%d", i)
		tokens = append([]string{token}, tokens...)
		require.NoError(t, db.WithContext(ctx).Create(&CalculatedBackfill{
			Token: token, ProfileToken: "meter", Status: status,
		}).Error)
	}

	// Count every query after setup. On main, over-limit valid lists execute SQL
	// and succeed, so these assertions fail by value rather than on a nil database.
	queries := 0
	var binds []any
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:backfill_status_queries", func(tx *gorm.DB) {
		queries++
		binds = append([]any(nil), tx.Statement.Vars...)
	}))
	for _, tc := range []struct {
		name     string
		statuses []string
	}{
		{"six entries", append(append([]string(nil), statuses...), BackfillQueued)},
		{"six queued", []string{BackfillQueued, BackfillQueued, BackfillQueued, BackfillQueued, BackfillQueued, BackfillQueued}},
		{"many repeats", func() []string {
			values := make([]string, 100000)
			for i := range values {
				values[i] = BackfillQueued
			}
			return values
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries = 0
			page, err := api.ListBackfills(ctx, ListBackfillsRequest{Statuses: tc.statuses})
			require.Zero(t, queries, "over-limit lists must be refused before any query")
			require.Nil(t, page)
			le, ok := limit.As(err)
			require.True(t, ok, "expected typed limit refusal, got %v", err)
			assert.Equal(t, "statuses", le.What)
			assert.Equal(t, len(tc.statuses), le.Got)
			assert.Equal(t, 5, le.Max)
			assert.Equal(t, "LIMIT_EXCEEDED", le.Extensions()["code"])
		})
	}

	queries = 0
	page, err := api.ListBackfills(ctx, ListBackfillsRequest{Statuses: statuses})
	require.NoError(t, err, "all five distinct statuses are accepted")
	require.Equal(t, 1, queries)
	var got []string
	for _, job := range page.Jobs {
		got = append(got, job.Token)
	}
	assert.Equal(t, tokens, got, "all five statuses are returned newest first")

	for i, status := range statuses {
		page, err := api.ListBackfills(ctx, ListBackfillsRequest{Statuses: []string{status}})
		require.NoError(t, err)
		require.Len(t, page.Jobs, 1)
		assert.Equal(t, fmt.Sprintf("job-%d", i), page.Jobs[0].Token, "each status filters correctly")
		singleBinds := append([]any(nil), binds...)
		page, err = api.ListBackfills(ctx, ListBackfillsRequest{Statuses: []string{status, status, status, status, status}})
		require.NoError(t, err, "duplicates within the raw bound are accepted")
		require.Len(t, page.Jobs, 1)
		assert.Equal(t, fmt.Sprintf("job-%d", i), page.Jobs[0].Token)
		assert.Equal(t, singleBinds, binds, "accepted duplicates add no SQL bind parameters")
	}
}
