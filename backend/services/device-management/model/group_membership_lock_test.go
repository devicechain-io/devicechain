// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Retain SQLite's SQL implementation but exercise the Postgres-only lock branch.
// This tests wiring and failure handling; the tagged suite tests real MVCC.
type membershipPostgresDialect struct{ gorm.Dialector }

func (membershipPostgresDialect) Name() string { return "postgres" }

func TestMembershipFamilyLockPrecedesMaintenance(t *testing.T) {
	for _, path := range []string{"attribute", "register"} {
		for _, refuse := range []bool{false, true} {
			name := path + "/granted"
			if refuse {
				name = path + "/refused"
			}
			t.Run(name, func(t *testing.T) {
				api, ctx := newScopingTestApi(t)
				id := seedDevice(t, api, ctx, "lock-device")
				g := createDynamicGroup(t, api, ctx, "lock-group", `attr["a"] == "x" && attr["b"] == "y"`)
				v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
				require.NoError(t, err)
				db := api.RDB.Database
				db.Dialector = membershipPostgresDialect{db.Dialector}
				stubAttributeProtocolPostgresSQL(t, db)
				denied := errors.New("family lock refused")
				locked, reads := false, 0
				require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:family-lock", func(tx *gorm.DB) {
					if strings.Contains(tx.Statement.SQL.String(), "dc_entity_attr_lock") {
						// The device attribute lock is a different lock; SQLite cannot run it.
						tx.Statement.SQL.Reset()
						tx.Statement.SQL.WriteString("SELECT 1")
						tx.Statement.Vars = nil
						return
					}
					if !strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
						return
					}
					if path == "attribute" {
						require.Contains(t, tx.Statement.SQL.String(), "pg_advisory_xact_lock_shared(")
					} else {
						require.Equal(t, "SELECT pg_advisory_xact_lock(?)", tx.Statement.SQL.String())
					}
					locked = true
					if refuse {
						tx.AddError(denied)
					}
					// SQLite cannot execute the advisory-lock function.
					tx.Statement.SQL.Reset()
					tx.Statement.SQL.WriteString("SELECT 1")
					tx.Statement.Vars = nil
				}))
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:maintenance-read", func(tx *gorm.DB) {
					if tx.Statement.Table == "entity_group_facet_refs" || tx.Statement.Table == "entity_group_memberships" {
						reads++
						require.True(t, locked, "maintenance read before family lock")
						require.False(t, refuse, "maintenance continued after lock refusal")
					}
				}))
				if path == "attribute" {
					value := "x"
					_, err = api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "lock-device",
						Scope: "SHARED", AttrKey: "a", ValueType: "STRING", Value: &value})
				} else {
					err = api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version)
				}
				require.True(t, locked, "Postgres maintenance must acquire the lock")
				if refuse {
					require.ErrorIs(t, err, denied)
					require.Zero(t, reads)
					var attrs int64
					require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).Where("entity_id = ?", id).Count(&attrs).Error)
					require.Zero(t, attrs, "a refused recompute must roll back the attribute write")
				} else {
					require.NoError(t, err)
					require.Positive(t, reads, "counterweight must actually reach maintenance")
				}
			})
		}
	}
}

// These maintenance tests retain SQLite's SQL builder while selecting Postgres's
// lock branches. Emulate only the attribute protocol's Postgres functions; the
// integration suite checks their real transaction and sequence semantics.
func stubAttributeProtocolPostgresSQL(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:attribute-protocol-functions", func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "dc_entity_attr_lock") || strings.Contains(sql, "set_config('devicechain.attr_protocol'") {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1")
			tx.Statement.Vars = nil
		}
	}))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:attribute-generation-sequence", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "FROM pg_locks") {
			// This fixture emulates successful acquisition of K_dev above.
			// Missing ownership and savepoint rollback have their own tests.
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1")
			tx.Statement.Vars = nil
		} else if strings.Contains(tx.Statement.SQL.String(), "nextval(") {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT COALESCE(MAX(generation), 0) + 1 FROM entity_attribute_generations")
			tx.Statement.Vars = nil
		}
	}))
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:attribute-generation-upsert", func(tx *gorm.DB) {
		if tx.Statement.Table != "entity_attribute_generations" {
			return
		}
		c := tx.Statement.Clauses["ON CONFLICT"]
		conflict, ok := c.Expression.(clause.OnConflict)
		if !ok {
			return
		}
		for i := range conflict.DoUpdates {
			if conflict.DoUpdates[i].Column.Name == "generation" {
				conflict.DoUpdates[i].Value = gorm.Expr("excluded.generation")
			}
		}
		c.Expression = conflict
		tx.Statement.Clauses["ON CONFLICT"] = c
	}))
}
