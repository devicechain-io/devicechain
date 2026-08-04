// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The tenant purge, driven against the schema an instance actually has (ADR-077).
//
// Everything else that tests core/tenantpurge runs on synthetic catalogs or on SQLite,
// which is enough to pin the classification rules and the generated SQL but cannot
// answer the question that matters here: does a sweep really erase a tenant from ten
// functional areas of real Postgres, and does it leave every other tenant alone.
//
// So this migrates every area's chain into one database — the same thing an instance
// bootstrap does, and the same thing `migrationdiff -mode verify` already does — seeds
// two tenants across the shapes that are hard, and sweeps exactly one of them.
//
// 🔑 THE SECOND TENANT IS THE POINT. A sweep that deleted everything, or a residual scan
// that could never see a row, both satisfy "the victim's rows are gone". The bystander
// is what makes those two failures visible: its rows are asserted present after the
// sweep, and a residual scan for the bystander is asserted NON-empty, so a scan that
// always reports clean fails here instead of certifying an erasure that never happened.
//
// Run it against a throwaway server (hack/migration-diff.sh starts one; note its port):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./... -run PurgeDrill -v
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

const (
	drillDB   = "dcpurgedrill"
	victim    = "victim-tenant"
	bystander = "bystander-tenant"
)

func drillConn(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err, "DC_IT_PGPORT must be a port number")
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	ctx := context.Background()

	for _, a := range areas {
		require.NoErrorf(t, migrateChain(ctx, a, host, port, "postgres", "postgres", drillDB),
			"migrating %s", a.name)
	}
	conn, err := openDatabase(host, port, "postgres", "postgres", drillDB)
	require.NoError(t, err)
	t.Cleanup(func() { closeDatabase(conn) })
	return conn, ctx
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	require.NoErrorf(t, db.Exec(sql, args...).Error, "seeding: %s", sql)
}

func count(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(
		fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, where), args...).Scan(&n).Error)
	return n
}

// seedTenant writes one tenant's rows across the shapes that are hard to erase: a
// foreign-key pair whose correct delete order is the reverse of its alphabetical one, a
// second foreign-key pair, event-processing's plain `tenant` column that the gorm
// tenant-scope callbacks do not recognise, and a many2many join carrying nothing that
// names a tenant at all.
//
// ids are supplied explicitly and derived from base so the two tenants never collide.
func seedTenant(t *testing.T, db *gorm.DB, tenant string, base int) {
	t.Helper()

	mustExec(t, db, `INSERT INTO "device-management".area_types (id, tenant_id, token)
	             VALUES (?, ?, ?)`, base+1, tenant, tenant+"-at")
	mustExec(t, db, `INSERT INTO "device-management".areas (id, tenant_id, token, area_type_id)
	             VALUES (?, ?, ?, ?)`, base+2, tenant, tenant+"-area", base+1)

	mustExec(t, db, `INSERT INTO "device-management".devices (id, tenant_id, token)
	             VALUES (?, ?, ?)`, base+3, tenant, tenant+"-dev")
	mustExec(t, db, `INSERT INTO "device-management".device_credentials
	               (id, tenant_id, token, device_id, credential_type, credential_id)
	             VALUES (?, ?, ?, ?, 'token', ?)`,
		base+4, tenant, tenant+"-cred", base+3, tenant+"-cid")

	mustExec(t, db, `INSERT INTO "event-processing".device_rosters
	               (tenant, device_token, profile_token, expected_since, deleted, last_event_at)
	             VALUES (?, ?, 'p', now(), false, now())`, tenant, tenant+"-dev")

	mustExec(t, db, `INSERT INTO "user-management".iam_memberships (id, identity_id, tenant_id)
	             VALUES (?, ?, ?)`, base+5, sharedIdentityID, tenant)
	mustExec(t, db, `INSERT INTO "user-management".iam_membership_tenant_roles (membership_id, role_id)
	             VALUES (?, ?)`, base+5, sharedRoleID)
}

const (
	sharedIdentityID = 9001
	sharedRoleID     = 9002
)

// seedShared writes the instance-global rows both tenants reference. They are the
// exemption registry's claims made concrete: an identity is a PERSON who may belong to
// several tenants, and a role is an instance-wide catalog entry. A purge that took
// either would delete a real user because someone else's tenant was removed.
func seedShared(t *testing.T, db *gorm.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO "user-management".iam_identities (id, email, password_hash)
	             VALUES (?, 'shared@example.com', 'x')`, sharedIdentityID)
	mustExec(t, db, `INSERT INTO "user-management".iam_roles (id, token, scope)
	             VALUES (?, 'drill-role', 'tenant')`, sharedRoleID)
}

// tenantRowCounts is every seeded location, as (label, table, where-clause).
func tenantRowCounts(tenant string) []struct{ label, table, where string } {
	return []struct{ label, table, where string }{
		{"device-management.area_types", `"device-management".area_types`, "tenant_id = ?"},
		{"device-management.areas", `"device-management".areas`, "tenant_id = ?"},
		{"device-management.devices", `"device-management".devices`, "tenant_id = ?"},
		{"device-management.device_credentials", `"device-management".device_credentials`, "tenant_id = ?"},
		{"event-processing.device_rosters", `"event-processing".device_rosters`, "tenant = ?"},
		{"user-management.iam_memberships", `"user-management".iam_memberships`, "tenant_id = ?"},
	}
}

func TestPurgeDrillErasesOneTenantAndLeavesTheOther(t *testing.T) {
	db, ctx := drillConn(t)

	// Start from a known-empty state so a previous run cannot make this one look right.
	for _, tenant := range []string{victim, bystander} {
		plan, err := tenantpurge.Classify(ctx, db)
		require.NoError(t, err)
		_, err = tenantpurge.Sweep(ctx, db, plan, tenant, nil)
		require.NoError(t, err)
	}
	mustExec(t, db, `DELETE FROM "user-management".iam_identities WHERE id = ?`, sharedIdentityID)
	mustExec(t, db, `DELETE FROM "user-management".iam_roles WHERE id = ?`, sharedRoleID)

	seedShared(t, db)
	seedTenant(t, db, victim, 1000)
	seedTenant(t, db, bystander, 2000)

	// The seed itself has to be proven, or an INSERT that silently did nothing would
	// make the erasure assertions below pass against an empty database.
	for _, c := range tenantRowCounts(victim) {
		require.Equalf(t, int64(1), count(t, db, c.table, c.where, victim),
			"seed did not land in %s — the erasure assertions would be vacuous", c.label)
	}
	joinBefore := count(t, db, `"user-management".iam_membership_tenant_roles`,
		"membership_id IN (SELECT id FROM \"user-management\".iam_memberships WHERE tenant_id = ?)", victim)
	require.Equal(t, int64(1), joinBefore, "seed did not land in the many2many join")

	plan, err := tenantpurge.Classify(ctx, db)
	require.NoError(t, err)

	res, err := tenantpurge.Sweep(ctx, db, plan, victim, nil)
	require.NoError(t, err, "the sweep must survive the real foreign-key graph")
	t.Logf("swept %d rows across %d tables", res.Rows, len(res.Tables))
	assert.False(t, res.Complete(),
		"detect_snapshots is registered deferred, so no sweep of this schema may report a "+
			"complete erasure")

	// The victim is gone everywhere.
	for _, c := range tenantRowCounts(victim) {
		assert.Zerof(t, count(t, db, c.table, c.where, victim), "%s still holds the victim", c.label)
	}
	assert.Zero(t, count(t, db, `"user-management".iam_membership_tenant_roles`,
		"membership_id = ?", 1005),
		"the many2many join row carries nothing naming a tenant; only the foreign key reaches it")

	// The bystander is untouched. This is the control: a sweep with no predicate at all
	// would satisfy every assertion above.
	for _, c := range tenantRowCounts(bystander) {
		assert.Equalf(t, int64(1), count(t, db, c.table, c.where, bystander),
			"%s lost the bystander's row — the sweep is not tenant-scoped", c.label)
	}
	assert.Equal(t, int64(1), count(t, db, `"user-management".iam_membership_tenant_roles`,
		"membership_id = ?", 2005), "the bystander's join row was taken with the victim's")

	// The shared rows the exemption registry claims are safe really were.
	assert.Equal(t, int64(1), count(t, db, `"user-management".iam_identities`, "id = ?", sharedIdentityID),
		"the purge deleted a PERSON because one of their tenants was removed")
	assert.Equal(t, int64(1), count(t, db, `"user-management".iam_roles`, "id = ?", sharedRoleID),
		"the purge deleted an instance-wide role")

	// --- and the residual scan, against the state the sweep just produced -----------
	//
	// Folded into this test rather than written as its own, because a separate test
	// would depend on this one having run first, and Go gives no such guarantee across
	// a package. Sharing the state here makes the dependency explicit instead of lucky.

	clean, err := tenantpurge.Residue(ctx, db, plan, victim)
	require.NoError(t, err)
	assert.Zerof(t, clean.Rows, "the residual scan found the swept tenant still present: %+v",
		clean.Tables)

	// 🔑 The non-vacuity control for the assertion immediately above. A Residue that
	// always returned zero — a broken predicate, a query against the wrong database —
	// would satisfy it and go on certifying every erasure that ever ran. The bystander
	// demonstrably has rows, so the scan must find them.
	dirty, err := tenantpurge.Residue(ctx, db, plan, bystander)
	require.NoError(t, err)
	assert.NotZero(t, dirty.Rows,
		"the residual scan reported a tenant with live rows as clean, so it cannot see residue "+
			"at all and its clean verdict for the victim means nothing")
}
