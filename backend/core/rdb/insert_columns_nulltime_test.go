// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// colNullTime is the smallest tenant-scoped, audit-exempt model with a nullable time column.
type colNullTime struct {
	gorm.Model
	TenantId string `gorm:"uniqueIndex:uq_col_nt_key,priority:1;not null;size:128"`
	Key      string `gorm:"uniqueIndex:uq_col_nt_key,priority:2"`
	Seen     sql.NullTime
}

func (colNullTime) AuditExempt() bool { return true }

func (colNullTime) TableName() string { return "col_null_times" }

var colNullTimes = NewColumnUpsert(func(r *colNullTime) *string { return &r.TenantId }, []string{"tenant_id", "key"},
	Upsert{Set: []Assignment{SetTo("seen", "excluded.seen"), SetTo("updated_at", "excluded.updated_at")}},
	TimeColumn("created_at", func(r *colNullTime) time.Time { return r.CreatedAt }),
	TimeColumn("updated_at", func(r *colNullTime) time.Time { return r.UpdatedAt }),
	TextColumn("key", func(r *colNullTime) string { return r.Key }),
	NullTimeColumn("seen", func(r *colNullTime) sql.NullTime { return r.Seen }),
)

// A NULL time stays NULL, a valid one is stored, and an update can move either way.
func TestNullTimeColumnWritesNullAndValidTimes(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&colNullTime{}))
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	row := func(key string, seen sql.NullTime) *colNullTime {
		return &colNullTime{Model: gorm.Model{CreatedAt: at, UpdatedAt: at}, Key: key, Seen: seen}
	}
	ctx := tenantCtx("acme")
	_, err := colNullTimes.Upsert(db.WithContext(ctx), []*colNullTime{
		row("null", sql.NullTime{}), row("set", sql.NullTime{Time: at, Valid: true}),
		row("to-null", sql.NullTime{Time: at, Valid: true}), row("to-set", sql.NullTime{})})
	require.NoError(t, err)
	_, err = colNullTimes.Upsert(db.WithContext(ctx), []*colNullTime{
		row("to-null", sql.NullTime{}), row("to-set", sql.NullTime{Time: at.Add(time.Hour), Valid: true})})
	require.NoError(t, err)

	var got []colNullTime
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).Order("key").Find(&got).Error)
	seen := map[string]sql.NullTime{}
	for _, r := range got {
		seen[r.Key] = r.Seen
	}
	require.False(t, seen["null"].Valid)
	require.True(t, seen["set"].Valid)
	require.True(t, seen["set"].Time.Equal(at))
	require.False(t, seen["to-null"].Valid)
	require.True(t, seen["to-set"].Valid)
	require.True(t, seen["to-set"].Time.Equal(at.Add(time.Hour)))
}
