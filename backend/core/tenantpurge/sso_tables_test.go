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

// The single-sign-on tables user-management gained. Their DDL is mirrored here by hand,
// because core cannot import a service's migration; the REAL tables are held to the same
// classification by the coverage gate (hack/migration-diff.sh coverage), which classifies
// the freshly migrated Postgres schema. This test pins the part that gate cannot say in
// words: WHICH table is swept by what, and that the sweep leaves the instance-global ones.

func ssoCatalog() ([]column, []constraint) {
	const s = "user-management"
	cols := []column{
		col(s, "iam_identities", "id", "bigint"),
		col(s, "iam_roles", "id", "bigint"),
		col(s, "iam_memberships", "id", "bigint"),
		col(s, "iam_memberships", "tenant_id", "character varying"),
		col(s, "iam_memberships", "managed_by", "bigint"),

		col(s, "iam_idp_connections", "id", "bigint"),
		col(s, "iam_idp_connection_tenants", "connection_id", "bigint"),
		col(s, "iam_idp_connection_tenants", "tenant_id", "character varying"),
		col(s, "iam_idp_connection_domains", "id", "bigint"),
		col(s, "iam_idp_connection_domains", "connection_id", "bigint"),
		col(s, "iam_idp_role_mappings", "id", "bigint"),
		col(s, "iam_idp_role_mappings", "connection_id", "bigint"),
		col(s, "iam_idp_role_mappings", "tenant_id", "character varying"),
		col(s, "iam_idp_role_mappings", "role_id", "bigint"),
		col(s, "iam_identity_links", "id", "bigint"),
		col(s, "iam_identity_links", "identity_id", "bigint"),
		col(s, "iam_identity_links", "connection_id", "bigint"),
		col(s, "iam_sso_link_requests", "id", "bigint"),
		col(s, "iam_sso_link_requests", "connection_id", "bigint"),
	}
	fks := []constraint{
		fk(s, "iam_idp_connection_tenants", "connection_id", s, "iam_idp_connections", "id"),
		fk(s, "iam_idp_connection_domains", "connection_id", s, "iam_idp_connections", "id"),
		{Schema: s, Table: "iam_idp_role_mappings", Columns: "connection_id,tenant_id",
			ParentSchema: s, ParentTable: "iam_idp_connection_tenants", ParentColumns: "connection_id,tenant_id"},
		fk(s, "iam_idp_role_mappings", "role_id", s, "iam_roles", "id"),
		fk(s, "iam_identity_links", "identity_id", s, "iam_identities", "id"),
		fk(s, "iam_identity_links", "connection_id", s, "iam_idp_connections", "id"),
		fk(s, "iam_sso_link_requests", "connection_id", s, "iam_idp_connections", "id"),
	}
	return cols, fks
}

func TestSsoTablesAreClassifiedAndTheInstanceGlobalOnesAreLeftAlone(t *testing.T) {
	cols, fks := ssoCatalog()
	plan := planOf(t, cols, fks) // planOf also fails if ANY table is unclassified

	for _, name := range []string{"iam_idp_connection_tenants", "iam_idp_role_mappings"} {
		e := classOf(t, plan, "user-management", name)
		assert.Equalf(t, ClassDirect, e.Class, "%s carries a tenant column and must be swept by it", name)
		assert.Equal(t, "tenant_id", e.Column)
	}
	for _, name := range []string{
		"iam_idp_connections", "iam_idp_connection_domains", "iam_identity_links", "iam_sso_link_requests",
	} {
		e := classOf(t, plan, "user-management", name)
		assert.Equalf(t, ClassExempt, e.Class, "%s holds no single tenant's data", name)
		assert.NotEmptyf(t, e.Reason, "%s: an exemption without a reason has not earned its place", name)
	}
}

// TestTenantPurgeRemovesSsoRows drives the sweep over real tables that ENFORCE the foreign
// keys, with two tenants served by one connection. Only the purged tenant's rows go; the
// connection, its domains, the people and their links, and the other tenant's rows stay.
func TestTenantPurgeRemovesSsoRows(t *testing.T) {
	db := sqliteDB(t)
	// The exemption registry is keyed by the real functional-area schema name, so the tables
	// live in a database ATTACHED under that name. One connection, because ATTACH is
	// per-connection and gorm pools them.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "user-management"`).Error)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error)

	for _, ddl := range []string{
		`CREATE TABLE iam_identities (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE iam_roles (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE iam_memberships (id INTEGER PRIMARY KEY, tenant_id TEXT, managed_by INTEGER)`,
		`CREATE TABLE iam_idp_connections (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE iam_idp_connection_tenants (
			connection_id INTEGER REFERENCES iam_idp_connections(id) ON DELETE CASCADE,
			tenant_id TEXT, PRIMARY KEY (connection_id, tenant_id))`,
		`CREATE TABLE iam_idp_connection_domains (
			id INTEGER PRIMARY KEY, connection_id INTEGER REFERENCES iam_idp_connections(id) ON DELETE CASCADE)`,
		`CREATE TABLE iam_idp_role_mappings (
			id INTEGER PRIMARY KEY, connection_id INTEGER, tenant_id TEXT,
			role_id INTEGER REFERENCES iam_roles(id) ON DELETE RESTRICT,
			FOREIGN KEY (connection_id, tenant_id)
				REFERENCES iam_idp_connection_tenants(connection_id, tenant_id) ON DELETE CASCADE)`,
		`CREATE TABLE iam_identity_links (
			id INTEGER PRIMARY KEY,
			identity_id INTEGER REFERENCES iam_identities(id) ON DELETE CASCADE,
			connection_id INTEGER REFERENCES iam_idp_connections(id) ON DELETE CASCADE)`,
		`CREATE TABLE iam_sso_link_requests (
			id INTEGER PRIMARY KEY, connection_id INTEGER REFERENCES iam_idp_connections(id) ON DELETE CASCADE)`,

		`INSERT INTO iam_identities (id) VALUES (1)`,
		`INSERT INTO iam_roles (id) VALUES (10)`,
		`INSERT INTO iam_idp_connections (id) VALUES (1)`,
		`INSERT INTO iam_idp_connection_tenants (connection_id, tenant_id) VALUES (1,'acme'),(1,'globex')`,
		`INSERT INTO iam_idp_connection_domains (id, connection_id) VALUES (1,1)`,
		`INSERT INTO iam_idp_role_mappings (id, connection_id, tenant_id, role_id) VALUES (1,1,'acme',10),(2,1,'globex',10)`,
		`INSERT INTO iam_identity_links (id, identity_id, connection_id) VALUES (1,1,1)`,
		`INSERT INTO iam_sso_link_requests (id, connection_id) VALUES (1,1)`,
		`INSERT INTO iam_memberships (id, tenant_id, managed_by) VALUES (1,'acme',1),(2,'globex',1)`,
	} {
		ddl = strings.NewReplacer("CREATE TABLE ", `CREATE TABLE "user-management".`, "INSERT INTO ", `INSERT INTO "user-management".`).Replace(ddl)
		require.NoError(t, db.Exec(ddl).Error, ddl)
	}

	cols, fks := ssoCatalog()
	plan := planOf(t, cols, fks)

	res, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	assert.Positive(t, res.Rows)

	rows := func(q string) int64 {
		var n int64
		q = strings.NewReplacer("FROM iam_", `FROM "user-management".iam_`).Replace(q)
		require.NoError(t, db.Raw(q).Scan(&n).Error)
		return n
	}
	assert.Zero(t, rows(`SELECT count(*) FROM iam_idp_connection_tenants WHERE tenant_id='acme'`))
	assert.Zero(t, rows(`SELECT count(*) FROM iam_idp_role_mappings WHERE tenant_id='acme'`))
	assert.Zero(t, rows(`SELECT count(*) FROM iam_memberships WHERE tenant_id='acme'`))

	// The survivors are the control: a sweep that took the whole connection, or the other
	// tenant, would still show the purged tenant's rows gone.
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_idp_connection_tenants WHERE tenant_id='globex'`))
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_idp_role_mappings WHERE tenant_id='globex'`))
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_memberships WHERE tenant_id='globex'`))
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_idp_connections`), "the connection is instance-global")
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_idp_connection_domains`))
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_identity_links`), "a person is not a tenant's to erase")
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_sso_link_requests`))
	assert.EqualValues(t, 1, rows(`SELECT count(*) FROM iam_identities`))
}
