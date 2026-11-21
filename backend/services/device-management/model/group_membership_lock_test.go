// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
