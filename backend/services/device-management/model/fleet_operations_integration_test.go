// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestFleetOperationJournalsOnPostgres runs the appended migration through the real chain on a
// real server and asserts what the SQLite unit tests can only approximate: the CHECKs, the
// partial unique indexes and the driver_protocol default.
func TestFleetOperationJournalsOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	tenant := fmt.Sprintf("fleetjournal%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)

	op := &FleetOperation{Token: "t1", Kind: "config.set", Name: "n", Dispatch: "READY", RequestDigest: "sha256:x",
		Intent: "{}", TargetKind: "DEVICE_LIST", TargetCount: 1, MaxPostsPerSecond: 10, EffPostsPerSecond: 10,
		SettleDeadlineS: 60, Initiator: "u", DriverProtocol: 1}
	require.NoError(t, db.WithContext(ctx).Create(op).Error)
	var stored FleetOperation
	require.NoError(t, db.WithContext(ctx).First(&stored, op.ID).Error)
	require.Equal(t, int32(1), stored.DriverProtocol)
	var nodefault int64
	require.NoError(t, db.WithContext(ctx).Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'device-management' AND table_name = 'fleet_operations' AND column_name = 'driver_protocol' AND column_default IS NOT NULL`).Scan(&nodefault).Error)
	require.Zero(t, nodefault, "driver_protocol has no default")
	require.Equal(t, "OPEN", stored.Evidence)

	dup := *op
	dup.ID = 0
	require.Error(t, db.WithContext(ctx).Create(&dup).Error, "(tenant, token) is unique")

	bad := *op
	bad.ID, bad.Token, bad.Dispatch = 0, "t2", "DONE"
	require.Error(t, db.WithContext(ctx).Create(&bad).Error, "dispatch is a closed vocabulary")
	require.Error(t, db.WithContext(ctx).Exec(`UPDATE fleet_operations SET driver_protocol = 0 WHERE id = ?`, op.ID).Error)

	id := int64(op.ID)
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 1, Posting: "PLANNED"}).Error)
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 2, Posting: "PLANNED", Prior: sql.NullString{String: "{}", Valid: true}}).Error,
		"a before-image exists only on a posted line")
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 3, Posting: "REFUSED", Settlement: sql.NullString{String: "FAILED", Valid: true}}).Error,
		"a settlement exists only on a posted line")
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 4, Posting: "POSTED", Settlement: sql.NullString{String: "AMBIGUOUS", Valid: true}}).Error)
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 4, Posting: "PLANNED"}).Error, "one line per ordinal")
}

// TestFleetOperationModelsMatchThePostgresSchema compares every live model field with the column
// the migration built on a real server. SQLite cannot: it stores int32 and int64 alike, so a
// model whose integer width drifted from the DDL would pass the unit comparison.
func TestFleetOperationModelsMatchThePostgresSchema(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	base := map[string]string{
		"integer": "int4", "bigint": "int8", "bigserial": "int8", "varchar": "varchar", "text": "text",
		"timestamptz": "timestamptz", "boolean": "bool",
	}
	for _, m := range []any{&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{}, &FleetOperationLine{}, &FleetOperationEvent{}} {
		stmt := &gorm.Statement{DB: db}
		require.NoError(t, stmt.Parse(m))
		columns, err := db.Migrator().ColumnTypes(m)
		require.NoError(t, err)
		byName := map[string]gorm.ColumnType{}
		for _, c := range columns {
			byName[c.Name()] = c
		}
		require.Len(t, byName, len(stmt.Schema.DBNames), stmt.Table)
		for _, f := range stmt.Schema.Fields {
			if f.DBName == "" {
				continue
			}
			col, ok := byName[f.DBName]
			require.True(t, ok, "%s.%s is not a column", stmt.Table, f.DBName)
			dt := string(db.Migrator().FullDataTypeOf(f).SQL)
			want := strings.ToLower(strings.Fields(dt)[0])
			size := ""
			if i := strings.Index(want, "("); i >= 0 {
				size = strings.TrimSuffix(want[i+1:], ")")
				want = want[:i]
			}
			require.Equal(t, base[want], col.DatabaseTypeName(), "%s.%s", stmt.Table, f.DBName)
			if size != "" && want == "varchar" {
				length, _ := col.Length()
				require.Equal(t, size, fmt.Sprint(length), "%s.%s", stmt.Table, f.DBName)
			}
			nullable, _ := col.Nullable()
			require.Equal(t, !f.NotNull && !f.PrimaryKey, nullable, "%s.%s nullability", stmt.Table, f.DBName)
		}
	}
}
