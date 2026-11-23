// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var fleetModels = []any{
	&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{},
	&FleetOperationLine{}, &FleetOperationEvent{},
}

type columnShape struct {
	Type     string
	Nullable bool
	Default  string
	Length   int64
}

func tableShape(t *testing.T, db *gorm.DB, table string) map[string]columnShape {
	t.Helper()
	types, err := db.Migrator().ColumnTypes(table)
	require.NoError(t, err)
	shape := map[string]columnShape{}
	for _, c := range types {
		nullable, _ := c.Nullable()
		def, _ := c.DefaultValue()
		length, _ := c.Length()
		shape[c.Name()] = columnShape{Type: c.DatabaseTypeName(), Nullable: nullable, Default: def, Length: length}
	}
	return shape
}

// checkConstraints returns name -> expression of every CHECK in the table's stored CREATE statement.
func checkConstraints(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var ddl string
	require.NoError(t, db.Raw(`SELECT sql FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&ddl).Error)
	out := map[string]string{}
	for rest := ddl; ; {
		i := strings.Index(rest, "CONSTRAINT ")
		if i < 0 {
			return out
		}
		rest = rest[i+len("CONSTRAINT "):]
		name := strings.Trim(rest[:strings.IndexAny(rest, " \t\n")], "`\"")
		k := strings.Index(rest, "CHECK")
		if k < 0 || k > 80 {
			continue
		}
		open := strings.Index(rest[k:], "(") + k
		depth := 0
		for j := open; j < len(rest); j++ {
			switch rest[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					out[name] = strings.Join(strings.Fields(rest[open+1:j]), " ")
					rest = rest[j:]
					j = len(rest)
				}
			}
		}
	}
}

func indexNames(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	var names []string
	require.NoError(t, db.Raw(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name = ? AND sql IS NOT NULL ORDER BY name`, table).Scan(&names).Error)
	sort.Strings(names)
	return names
}

// TestFleetOperationModelsMatchTheMigration is the guard between a migration's snapshot and the
// live model: the migration declares its own copy of every shape, so nothing but this test notices
// a model that drifted from the DDL it is supposed to read and write.
func TestFleetOperationModelsMatchTheMigration(t *testing.T) {
	migrated := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, schema.NewFleetOperationsSchema().Migrate(migrated))
	live := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, live.AutoMigrate(fleetModels...))

	for _, table := range []string{"fleet_operations", "fleet_operation_runtime", "fleet_operation_contracts",
		"fleet_operation_lines", "fleet_operation_events"} {
		want := tableShape(t, migrated, table)
		got := tableShape(t, live, table)
		require.NotEmpty(t, want, table)
		require.Equal(t, want, got, table)
		// The migration adds its partial indexes by hand, so the live models (which declare only
		// tag indexes) must be a subset of what the migration built.
		for _, name := range indexNames(t, live, table) {
			require.Contains(t, indexNames(t, migrated, table), name, table)
		}
		// And the CHECK constraints must be the same, expression for expression.
		require.Equal(t, checkConstraints(t, migrated, table), checkConstraints(t, live, table), table)
		if table == "fleet_operations" || table == "fleet_operation_lines" {
			require.NotEmpty(t, checkConstraints(t, migrated, table), "the comparison must not be vacuous: "+table)
		}
	}
}

func TestFleetOperationModelsAreInTheTenantScopeAndAuditJournalAsDesigned(t *testing.T) {
	db := partialupdatetest.NewSQLiteDB(t, fleetModels...)
	ctx := core.WithTenant(context.Background(), "acme")

	op := &FleetOperation{Token: "t1", Kind: "config.set", Name: "n", Dispatch: "PLANNING", RequestDigest: "sha256:x",
		Intent: "{}", TargetKind: "DEVICE_LIST", TargetCount: 1, MaxPostsPerSecond: 10, EffPostsPerSecond: 10,
		SettleDeadlineS: 60, Initiator: "u", DriverProtocol: int32(ledger.RequiredProtocol(ledger.ProtocolFeatures{}))}
	require.NoError(t, db.WithContext(ctx).Create(op).Error)
	require.Equal(t, "acme", op.TenantId, "the tenant is stamped from the context")

	audited := func(table string) int64 {
		var n int64
		require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events WHERE table_name LIKE ?`, "%"+table).Scan(&n).Error)
		return n
	}
	require.Equal(t, int64(1), audited("fleet_operations"), "the header is audited (negative control: the exemption is per model)")
	require.Equal(t, int64(0), audited("fleet_operation_contracts"))

	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationRuntime{OperationId: int64(op.ID)}).Error)
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationContract{OperationId: int64(op.ID), Version: 1, Digest: "d", Body: "{}",
		ApprovalKind: "HUMAN_DIRECT", PolicyVersion: 1, ProposedBy: "u"}).Error)
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: int64(op.ID), Ordinal: 1, Posting: "PLANNED",
		TargetId: sql.NullInt64{Int64: 5, Valid: true}}).Error)
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationEvent{OperationId: int64(op.ID), At: time.Now(), Kind: "CREATE"}).Error)
	// The contract records who approved what, so it IS audited; the rest are not.
	require.Equal(t, int64(1), audited("fleet_operation_contracts"), "the contract is audited")
	for _, table := range []string{"fleet_operation_runtime", "fleet_operation_lines", "fleet_operation_events"} {
		require.Zero(t, audited(table), "%s must stay out of the audit journal", table)
	}

	// Fail closed: a tenant-scoped table refuses a query with no tenant in the context.
	var rows []FleetOperationLine
	require.Error(t, db.Find(&rows).Error)
	// And another tenant sees none of acme's rows.
	other := core.WithTenant(context.Background(), "globex")
	require.NoError(t, db.WithContext(other).Find(&rows).Error)
	require.Empty(t, rows)
	require.NoError(t, db.WithContext(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
}
