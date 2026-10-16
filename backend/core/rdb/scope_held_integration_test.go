// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The HeldAnchors predicate, asked of a real PostgreSQL server: scope_held_test.go can only
// render the statement on SQLite, which has no arrays.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run Held -v
package rdb

import (
	"context"
	"sort"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

type itHeldDevice struct {
	gorm.Model
	TenantScoped
	DeviceToken  string
	ScopeAnchors Anchors `gorm:"type:text[]"`
}

func (itHeldDevice) ScopedBy() ScopeRule { return AnchorColumn("scope_anchors") }

type itHeldReading struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	DeviceToken string
	Name        string
}

func (itHeldReading) ScopedBy() ScopeRule {
	return HeldAnchors(AnchorHolder{Model: &itHeldDevice{}, Keys: []KeyPair{{Set: "device_token", Row: "device_token"}},
		Column: "scope_anchors"})
}

func heldItDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbheld"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "held"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20261016000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&itHeldDevice{}, &itHeldReading{}) },
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
	sys := mgr.DB(core.WithSystemContext(ctx))
	require.NoError(t, sys.Exec("TRUNCATE TABLE it_held_devices, it_held_readings").Error)
	return mgr.Database
}

func heldNames(t *testing.T, db *gorm.DB, ctx context.Context) []string {
	t.Helper()
	var rows []itHeldReading
	require.NoError(t, db.WithContext(ctx).Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

// 🔴 SCOPE LEAKAGE ACROSS CUSTOMERS AND TENANTS through a held stamp: Acme's member sees the
// readings of the devices whose holder row carries Acme — a device it shares with Globex
// included — and nothing else: not Globex's device's, not a device with an unstamped or
// empty holder, not a reading with no holder at all, not one whose holder is soft-deleted,
// and not tenant B's, although B's holder for the SAME device token carries Acme. Then the
// holder is restamped and the readings move with it, with no write to them.
func TestAHeldAnchorsReadOnPostgres(t *testing.T) {
	db := heldItDB(t)
	device := func(tenant, token string, anchors Anchors) *itHeldDevice {
		d := &itHeldDevice{DeviceToken: token, ScopeAnchors: anchors}
		require.NoError(t, db.WithContext(core.WithTenant(context.Background(), tenant)).Create(d).Error)
		return d
	}
	reading := func(tenant, token, name string) {
		require.NoError(t, db.WithContext(core.WithTenant(context.Background(), tenant)).
			Create(&itHeldReading{DeviceToken: token, Name: name}).Error)
	}
	device("A", "acme-1", Anchors{"customer:acme-corp"})
	device("A", "shared-1", Anchors{"customer:globex", "customer:acme-corp"})
	device("A", "globex-1", Anchors{"customer:globex"})
	device("A", "unstamped-1", nil)
	device("A", "empty-1", Anchors{})
	gone := device("A", "gone-1", Anchors{"customer:acme-corp"})
	require.NoError(t, db.WithContext(core.WithTenant(context.Background(), "A")).Delete(gone).Error)
	device("B", "globex-1", Anchors{"customer:acme-corp"}) // another tenant's holder, same token
	for _, d := range []string{"acme-1", "shared-1", "globex-1", "unstamped-1", "empty-1", "gone-1", "orphan-1"} {
		reading("A", d, d+"/temp")
	}
	reading("B", "globex-1", "b/globex-1/temp")

	acme := scopedCtx("A", "customer:acme-corp")
	require.Equal(t, []string{"acme-1/temp", "shared-1/temp"}, heldNames(t, db, acme))
	require.Equal(t, []string{"globex-1/temp", "shared-1/temp"}, heldNames(t, db, scopedCtx("A", "customer:globex")))
	require.Empty(t, heldNames(t, db, scopedCtx("A")), "an anchorless scope must see nothing")
	require.Empty(t, heldNames(t, db, scopedCtx("A", "group:acme-corp")), "another kind, same token")
	require.Equal(t, []string{"b/globex-1/temp"}, heldNames(t, db, scopedCtx("B", "customer:acme-corp")),
		"tenant B's member sees B's reading through B's holder only")
	// Negative control: unscoped, tenant A sees all seven of its own.
	require.Len(t, heldNames(t, db, unscopedCtx("A")), 7)

	// Restamp the holder; the reading follows with no write to it.
	require.NoError(t, db.WithContext(core.WithTenant(context.Background(), "A")).Model(&itHeldDevice{}).
		Where("device_token = ?", "globex-1").Update("scope_anchors", Anchors{"customer:acme-corp"}).Error)
	require.Equal(t, []string{"acme-1/temp", "globex-1/temp", "shared-1/temp"}, heldNames(t, db, acme))
	require.Equal(t, []string{"shared-1/temp"}, heldNames(t, db, scopedCtx("A", "customer:globex")))
}
