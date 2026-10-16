// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The AnchorColumn predicate, asked of a real PostgreSQL server. SQLite has no arrays, so
// scope_callback_test.go can only render the statement; this proves the rows it returns.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run Scope -v
package rdb

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

// itScopedAlarm is an alarm-shaped row that stamps its own anchors.
type itScopedAlarm struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name         string
	Acked        bool
	ScopeAnchors Anchors `gorm:"type:text[]"`
}

func (itScopedAlarm) ScopedBy() ScopeRule {
	return AnchorColumn("scope_anchors", AllowScopedUpdate, AllowScopedCreate)
}

func scopeItDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbscope"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "scope"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20261015000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&itScopedAlarm{}) },
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres",
				"sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	require.NoError(t, mgr.DB(core.WithSystemContext(ctx)).Exec("TRUNCATE TABLE it_scoped_alarms").Error)
	return mgr.Database
}

func alarmNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var rows []itScopedAlarm
	require.NoError(t, db.WithContext(ctx).Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

// 🔴 SCOPE LEAKAGE ACROSS CUSTOMERS AND TENANTS, on PostgreSQL: Acme's member sees the
// alarms stamped with Acme — including one shared with Globex — and nothing else: not
// Globex's, not an unstamped one (NULL or empty), and not tenant B's although it carries
// the same anchor. The stamp round-trips through rdb.Anchors.
func TestAnAnchorColumnReadOnPostgres(t *testing.T) {
	db := scopeItDB(t)
	seed := func(tenant, name string, anchors Anchors) {
		require.NoError(t, db.WithContext(core.WithTenant(context.Background(), tenant)).
			Create(&itScopedAlarm{Name: name, ScopeAnchors: anchors}).Error)
	}
	seed("A", "acme-high", Anchors{"customer:acme-corp"})
	seed("A", "shared", Anchors{"customer:globex", "customer:acme-corp"})
	seed("A", "globex-high", Anchors{"customer:globex"})
	seed("A", "unstamped", nil)
	seed("A", "empty", Anchors{})
	seed("B", "b-high", Anchors{"customer:acme-corp"})

	acme := scopedCtx("A", "customer:acme-corp")
	require.Equal(t, []string{"acme-high", "shared"}, alarmNames(t, db, acme))
	require.Equal(t, []string{"globex-high", "shared"}, alarmNames(t, db, scopedCtx("A", "customer:globex")))
	require.Empty(t, alarmNames(t, db, scopedCtx("A")), "an anchorless scope must see nothing")
	require.Empty(t, alarmNames(t, db, scopedCtx("A", "group:acme-corp")), "another kind, same token")
	// Negative control: unscoped, tenant A sees all five of its own.
	require.Equal(t, []string{"acme-high", "empty", "globex-high", "shared", "unstamped"}, alarmNames(t, db, unscopedCtx("A")))

	var got itScopedAlarm
	require.NoError(t, db.WithContext(acme).Where("name = ?", "shared").First(&got).Error)
	require.True(t, reflect.DeepEqual(got.ScopeAnchors, Anchors{"customer:globex", "customer:acme-corp"}), "%v", got.ScopeAnchors)

	// A permitted scoped update reaches only rows in scope: acknowledging everything as
	// Acme's member acknowledges Acme's two and leaves Globex's alone.
	res := db.WithContext(acme).Model(&itScopedAlarm{}).Where("acked = ?", false).Update("acked", true)
	require.NoError(t, res.Error)
	require.EqualValues(t, 2, res.RowsAffected)
	var globex itScopedAlarm
	require.NoError(t, db.WithContext(unscopedCtx("A")).Where("name = ?", "globex-high").First(&globex).Error)
	require.False(t, globex.Acked, "a scoped update reached another customer's row")

	// A permitted scoped create inside the scope lands, and is visible to its creator.
	require.NoError(t, db.WithContext(acme).Create(&itScopedAlarm{Name: "made-by-acme", ScopeAnchors: Anchors{"customer:acme-corp"}}).Error)
	require.Contains(t, alarmNames(t, db, acme), "made-by-acme")
}
