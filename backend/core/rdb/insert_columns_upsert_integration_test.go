// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The column-array UPSERT, asked of a real PostgreSQL server: the statement SQLite never
// renders (unnest, the text[] anchors column, `INSERT … AS t`, the $1 tenant term).
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run ColumnarUpsert -v
package rdb

import (
	"context"
	"math"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

// colUpPG initializes a functional area the way a service does, with col_ups migrated and
// emptied, and hands back a handle on it.
func colUpPG(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbcolupsert"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "col-upsert"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20261103000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&colUp{}) },
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
	db := mgr.DB(core.WithSystemContext(ctx))
	require.NoError(t, db.Exec("TRUNCATE TABLE col_ups RESTART IDENTITY").Error)
	return db
}

func TestColumnarUpsertStoresWhatTheGormUpsertStoresOnPostgres(t *testing.T) {
	db := colUpPG(t)
	upsertEquivalence(t, db)
}

func TestColumnarUpsertAnchorsEdgeCasesOnPostgres(t *testing.T) {
	db := colUpPG(t)
	lists := map[string]Anchors{
		"nil": nil, "empty": {}, "quote": {`"`}, "backslash": {`\`}, "comma": {","},
		"brace": {"{", "}"}, "blank element": {""}, "mixed": {`a"\,{}b`, "plain"}, "NULL word": {"NULL"},
	}
	var rows []*colUp
	for name, l := range lists {
		r := upRow(name, 1)
		r.Anchors = l
		rows = append(rows, r)
	}
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), rows)
	require.NoError(t, err)
	for _, r := range storedUps(t, db) {
		want := lists[r.Key]
		require.Equal(t, want == nil, r.Anchors == nil, "%s: NULL stays NULL, and only NULL", r.Key)
		if want != nil {
			require.Equal(t, want, r.Anchors, r.Key)
		}
	}
	// The server's own view of two of them: NULL is NULL and '{}' has no elements.
	var nulls, empties int64
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.Raw("SELECT count(*) FROM col_ups WHERE anchors IS NULL").Scan(&nulls).Error)
	require.NoError(t, sys.Raw("SELECT count(*) FROM col_ups WHERE cardinality(anchors) = 0").Scan(&empties).Error)
	require.EqualValues(t, 1, nulls)
	require.EqualValues(t, 1, empties)
}

// The sequence hands out ids in the order the batch names its rows: unnest emits the array
// elements in order and nothing in the statement reorders them.
func TestColumnarUpsertKeepsTokenOrderOnPostgres(t *testing.T) {
	db := colUpPG(t)
	base := []string{"q", "b", "z", "a", "m", "c", "y", "d", "x", "e", "w", "f"}
	var want []string
	for batch := range 3 { // a second and third batch: the order holds across statements too
		keys := make([]string, len(base))
		for i, k := range base {
			keys[i] = k + string(rune('0'+batch))
		}
		want = append(want, keys...)
		_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, keys...))
		require.NoError(t, err)
	}
	var got []colUp
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).Order("id").Find(&got).Error)
	order := make([]string, len(got))
	for i, r := range got {
		order[i] = r.Key
	}
	require.Equal(t, want, order)
}

func TestColumnarUpsertCountsOnlyWrittenRowsOnPostgres(t *testing.T) {
	db := colUpPG(t)
	ctx := tenantCtx("acme")
	_, err := colUps.Upsert(db.WithContext(ctx), upRows(10, "held", "moved", "equal"))
	require.NoError(t, err)
	n, err := colUps.Upsert(db.WithContext(ctx),
		[]*colUp{upRow("held", 4), upRow("moved", 11), upRow("equal", 10), upRow("new", 1)})
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
}

func TestColumnarUpsertRefusesAUint64AboveBigintOnPostgres(t *testing.T) {
	db := colUpPG(t)
	over := upRow("over", 1)
	over.Big = math.MaxInt64 + 1
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), []*colUp{upRow("ok", 1), over})
	require.ErrorIs(t, err, ErrColumnValue)
	require.Empty(t, storedUps(t, db))
}

// A tenant's guard cannot reach another tenant's row: both write the same key, and the
// update lands on the writer's own row only.
func TestColumnarUpsertWritesNothingAcrossTenantsOnPostgres(t *testing.T) {
	db := colUpPG(t)
	for _, tenant := range []string{"acme", "globex"} {
		_, err := colUps.Upsert(db.WithContext(tenantCtx(tenant)), upRows(1, "k"))
		require.NoError(t, err)
	}
	changed := upRow("k", 9)
	changed.Label = "acme-only"
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), []*colUp{changed})
	require.NoError(t, err)
	for _, r := range storedUps(t, db) {
		require.Equal(t, map[string]string{"acme": "acme-only", "globex": "l-k"}[r.TenantId], r.Label)
	}
}
