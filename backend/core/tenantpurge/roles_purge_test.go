// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantOwnedRoleIsPurgedWithItsTenant drives the sweep over real tables that ENFORCE
// their foreign keys (ADR-081): a catalogue role (tenant_id NULL) held in two tenants, and
// one role OWNED by each tenant. Purging acme removes acme's own role and acme's role
// assignments, and leaves the catalogue role and globex's role — and globex's assignments
// of both — exactly as they were.
//
// Before tenants could own roles, iam_roles was exempt and carried no tenant column, so a
// tenant's own role would have outlived it; the exemption's removal and the column are the
// two halves of this test's claim, and the user-management schema test pins the column.
func TestTenantOwnedRoleIsPurgedWithItsTenant(t *testing.T) {
	const s = "user-management"
	db := sqliteDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "user-management"`).Error)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error)

	for _, ddl := range []string{
		`CREATE TABLE iam_roles (id INTEGER PRIMARY KEY, tenant_id TEXT)`,
		`CREATE TABLE iam_memberships (id INTEGER PRIMARY KEY, tenant_id TEXT)`,
		`CREATE TABLE iam_membership_tenant_roles (
			membership_id INTEGER REFERENCES iam_memberships(id) ON DELETE RESTRICT,
			role_id INTEGER REFERENCES iam_roles(id) ON DELETE RESTRICT,
			PRIMARY KEY (membership_id, role_id))`,
		`INSERT INTO iam_roles (id, tenant_id) VALUES (10, NULL), (20, 'acme'), (30, 'globex')`,
		`INSERT INTO iam_memberships (id, tenant_id) VALUES (1, 'acme'), (2, 'globex')`,
		`INSERT INTO iam_membership_tenant_roles (membership_id, role_id) VALUES (1,10),(1,20),(2,10),(2,30)`,
	} {
		ddl = strings.NewReplacer("CREATE TABLE ", `CREATE TABLE "user-management".`,
			"INSERT INTO ", `INSERT INTO "user-management".`).Replace(ddl)
		require.NoError(t, db.Exec(ddl).Error, ddl)
	}

	plan := planOf(t, []column{
		col(s, "iam_roles", "id", "bigint"),
		col(s, "iam_roles", "tenant_id", "character varying"),
		col(s, "iam_memberships", "id", "bigint"),
		col(s, "iam_memberships", "tenant_id", "character varying"),
		col(s, "iam_membership_tenant_roles", "membership_id", "bigint"),
		col(s, "iam_membership_tenant_roles", "role_id", "bigint"),
	}, []constraint{
		fk(s, "iam_membership_tenant_roles", "membership_id", s, "iam_memberships", "id"),
		fk(s, "iam_membership_tenant_roles", "role_id", s, "iam_roles", "id"),
	})

	_, err = Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)

	ids := func(q string) []int64 {
		var out []int64
		q = strings.NewReplacer("FROM iam_", `FROM "user-management".iam_`).Replace(q)
		require.NoError(t, db.Raw(q).Scan(&out).Error)
		return out
	}
	assert.Equal(t, []int64{10, 30}, ids(`SELECT id FROM iam_roles ORDER BY id`),
		"the purge must remove acme's OWN role and leave the catalogue role and globex's role")
	assert.Equal(t, []int64{2}, ids(`SELECT id FROM iam_memberships ORDER BY id`))
	assert.Equal(t, []int64{10, 30},
		ids(`SELECT role_id FROM iam_membership_tenant_roles WHERE membership_id = 2 ORDER BY role_id`),
		"globex's assignments — of the catalogue role and of its own — must survive acme's purge")
	assert.Empty(t, ids(`SELECT role_id FROM iam_membership_tenant_roles WHERE membership_id = 1`))
}
