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
// 🔴 ONE PRODUCTION SHAPE IS PERMANENTLY OUT OF REACH HERE: compressed chunks. Compression
// is applied by event-management's runtime lifecycle reconciler, not by its migrations
// (default: on, after 7 days), so a database built from the chains alone never has one —
// and a DELETE against a compressed chunk is a genuinely different code path in
// TimescaleDB. It is covered instead by event-management's own integration tests, which
// compress deliberately and then assert the rows are physically gone from the internal
// compressed store rather than merely invisible through the hypertable.
//
// Run it against a throwaway server (hack/migration-diff.sh starts one; note its port):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./... -run PurgeDrill -v
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/devicechain-io/dc-user-management/purge"
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

	// 🔴 THE JOURNAL HAS TO BE SEEDED BY HAND, and until it was, this drill certified
	// the ADR-077 retention without a single row to retain. Every other row here is
	// written by raw INSERT, which bypasses the GORM callbacks — so no audit row is ever
	// generated, and a redaction that did nothing would pass every assertion below.
	// Two schemas, because the journal is core-owned and exists in all ten; both columns
	// that can name a person, because a redaction covering one reads as settled.
	for _, schema := range []string{"user-management", "device-management"} {
		mustExec(t, db, fmt.Sprintf(`INSERT INTO %q.audit_events
		       (occurred_time, tenant_id, category, actor, table_name, operation, entity_pk, entity_label, rows_affected)
		     VALUES (now(), ?, 'mutation', ?, 'devices', 'create', '1', ?, 1)`, schema),
			tenant, tenant+"@example.test", tenant+"-jane-doe")
	}

	mustExec(t, db, `INSERT INTO "event-processing".device_rosters
	               (tenant, device_token, profile_token, expected_since, deleted, last_event_at)
	             VALUES (?, ?, 'p', now(), false, now())`, tenant, tenant+"-dev")

	mustExec(t, db, `INSERT INTO "user-management".iam_memberships (id, identity_id, tenant_id)
	             VALUES (?, ?, ?)`, base+5, sharedIdentityID, tenant)
	mustExec(t, db, `INSERT INTO "user-management".iam_membership_tenant_roles (membership_id, role_id)
	             VALUES (?, ?)`, base+5, sharedRoleID)

	// Telemetry, which is the shape nothing else here has: a hypertable whose rows are
	// ALSO copied into a continuous aggregate's materialization. Three buckets a minute
	// apart, so the aggregate has something to group.
	for i := 0; i < 3; i++ {
		mustExec(t, db, `INSERT INTO "event-management".measurement_events
		               (tenant_id, event_id, payload_id, device_token, event_type,
		                occurred_time, name, value)
		             VALUES (?, ?, ?, ?, 1, now() - make_interval(mins => ?), 'temp', 20.5)`,
			tenant, []byte(fmt.Sprintf("%s-evt-%d", tenant, i)),
			[]byte(fmt.Sprintf("%s-pay-%d", tenant, i)), tenant+"-dev", 10+i)
	}
}

// materializationOf returns the hypertable holding a continuous aggregate's rows.
//
// 🔴 EVERY COUNT MUST GO THROUGH THIS, NEVER THROUGH THE AGGREGATE'S VIEW. The view is
// declared materialized_only = false, so it reads the materialization only BELOW the
// refresh watermark and recomputes from the raw hypertable above it. A count through the
// view taken after the raw rows are gone therefore reports zero whether or not the
// materialized copy survived — which is the precise reading that would certify a purge
// that retained the tenant's aggregate buckets forever.
func materializationOf(t *testing.T, db *gorm.DB, viewSchema, viewName string) string {
	t.Helper()
	var mat string
	require.NoError(t, db.Raw(`
		SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
		FROM timescaledb_information.continuous_aggregates
		WHERE view_schema = ? AND view_name = ?`, viewSchema, viewName).Scan(&mat).Error)
	require.NotEmptyf(t, mat, "%s.%s is not a continuous aggregate in this database — every "+
		"assertion about its materialization would be vacuous", viewSchema, viewName)
	return mat
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

// seededLocation is one place seedTenant writes, with how many rows it puts there.
type seededLocation struct {
	label, table, where string
	rows                int64
}

// tenantRowCounts is every seeded location. The row count is carried per location rather
// than assumed to be one, so a location seeded with several rows cannot quietly pass an
// assertion written for a single one.
func tenantRowCounts() []seededLocation {
	return []seededLocation{
		{"device-management.area_types", `"device-management".area_types`, "tenant_id = ?", 1},
		{"device-management.areas", `"device-management".areas`, "tenant_id = ?", 1},
		{"device-management.devices", `"device-management".devices`, "tenant_id = ?", 1},
		{"device-management.device_credentials", `"device-management".device_credentials`, "tenant_id = ?", 1},
		{"event-processing.device_rosters", `"event-processing".device_rosters`, "tenant = ?", 1},
		{"user-management.iam_memberships", `"user-management".iam_memberships`, "tenant_id = ?", 1},
		{"event-management.measurement_events", `"event-management".measurement_events`, "tenant_id = ?", 3},
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

	// 🔴 THE JOURNAL HAS TO BE CLEARED BY HAND, and the reason is the change under test.
	// The preamble above resets by SWEEPING, which worked for every table while every
	// table was deleted. audit_events is now RETAINED — the sweep empties two columns and
	// keeps the row — so a second run of this drill found three journal rows where it
	// asserts one, having reset everything else perfectly. Any harness that leans on the
	// sweep to reset state has the same problem the day a table becomes retained.
	for _, a := range areas {
		mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.audit_events WHERE tenant_id IN (?, ?)`, a.name),
			victim, bystander)
	}

	seedShared(t, db)
	seedTenant(t, db, victim, 1000)
	seedTenant(t, db, bystander, 2000)

	// Materialize the telemetry the seed just wrote, so the continuous aggregate holds a
	// physical copy of both tenants' buckets BEFORE the sweep runs. Without this the
	// materialization is empty and every assertion about it below passes for the wrong
	// reason. The call cannot run inside a transaction, which is why it is a bare Exec.
	mustExec(t, db, `CALL refresh_continuous_aggregate('event-management.measurement_rollups', NULL, NULL)`)
	rollupMat := materializationOf(t, db, "event-management", "measurement_rollups")

	// The seed itself has to be proven, or an INSERT that silently did nothing would
	// make the erasure assertions below pass against an empty database.
	for _, c := range tenantRowCounts() {
		require.Equalf(t, c.rows, count(t, db, c.table, c.where, victim),
			"seed did not land in %s — the erasure assertions would be vacuous", c.label)
	}
	rollupBefore := count(t, db, rollupMat, "tenant_id = ?", victim)
	require.NotZero(t, rollupBefore,
		"the refresh materialized nothing, so the aggregate assertions below would hold over an "+
			"empty table and prove nothing about the erasure")
	bystanderRollupBefore := count(t, db, rollupMat, "tenant_id = ?", bystander)
	require.NotZero(t, bystanderRollupBefore, "the bystander's buckets must exist to be preserved")
	joinBefore := count(t, db, `"user-management".iam_membership_tenant_roles`,
		"membership_id IN (SELECT id FROM \"user-management\".iam_memberships WHERE tenant_id = ?)", victim)
	require.Equal(t, int64(1), joinBefore, "seed did not land in the many2many join")

	plan, err := tenantpurge.Classify(ctx, db)
	require.NoError(t, err)

	// 🔑 THE MECHANISM, ASSERTED SEPARATELY FROM THE OUTCOME. That the victim's buckets
	// end up gone is checked below, but a hand-written delete would satisfy that too. The
	// claim being made here is that the PLAN reaches the materialization — that a purge of
	// any database with a continuous aggregate in it erases the aggregate's copy without
	// anyone having written a step naming that aggregate.
	var matEntry *tenantpurge.Entry
	for i, e := range plan.Entries {
		if e.Table.String() == strings.ReplaceAll(rollupMat, `"`, "") {
			matEntry = &plan.Entries[i]
		}
	}
	require.NotNilf(t, matEntry, "%s is not in the plan: the aggregate's materialized rows are "+
		"reached by nothing, and a sweep of the raw hypertable leaves them in place forever", rollupMat)
	require.Equal(t, tenantpurge.ClassDirect, matEntry.Class)
	assert.Contains(t, matEntry.Describe(), "measurement_rollups",
		"the generated relation name means nothing on its own; the plan must carry the aggregate")

	res, err := tenantpurge.Sweep(ctx, db, plan, victim, nil)
	require.NoError(t, err, "the sweep must survive the real foreign-key graph")
	t.Logf("swept %d rows across %d tables", res.Rows, len(res.Tables))
	assert.True(t, res.Complete(),
		"no table in this schema is registered deferred any more, so the sweep has nothing left "+
			"behind to report — detect_snapshots became ClassExternal when the `detect` store "+
			"started evicting the tenant from the running engine")

	// 🔑 AND THAT IS ONLY SAFE BECAUSE THE STORE EXISTS. Complete() flipping to true here is
	// the sweep saying "I left nothing behind", which is a much weaker claim than "nothing is
	// left": the external table's data is erased by a store this sweep cannot see, on a
	// database it does not own. So the drill asserts the sweep still SEES the table and still
	// says who erases it — a table that dropped out of the plan, or an entry that lost its
	// store name, would leave the same green result over an erasure nobody performs.
	var detectEntry *tenantpurge.Entry
	for i, e := range plan.Entries {
		if e.Table.String() == "event-processing.detect_snapshots" {
			detectEntry = &plan.Entries[i]
		}
	}
	require.NotNil(t, detectEntry, "detect_snapshots is not in the plan at all, so nothing "+
		"records that the DETECT engine's checkpoint holds tenant data")
	assert.Equal(t, tenantpurge.ClassExternal, detectEntry.Class)
	assert.Equal(t, "detect", detectEntry.Store,
		"the entry must still name the store that erases it; an unnamed one is an unfalsifiable "+
			"claim that something, somewhere, handles it")

	// 🔴 THE JOURNAL IS THE ONE THING THE SWEEP KEEPS, and both halves of that are the
	// decision. Sweeping it would destroy the evidence that the deletion happened;
	// keeping it whole would retain the tenant's people by name past the erasure that
	// record certifies. Asserting only that the rows survive would pass with the
	// identifiers intact, and asserting only that the names are gone would pass with the
	// rows deleted — so both are checked, in both schemas, for the victim.
	for _, schema := range []string{"user-management", "device-management"} {
		tbl := fmt.Sprintf("%q.audit_events", schema)
		assert.Equalf(t, int64(1), count(t, db, tbl, "tenant_id = ?", victim),
			"%s dropped the victim's journal row: the erasure has no evidence", schema)
		assert.Zerof(t, count(t, db, tbl,
			"tenant_id = ? AND (actor <> '' OR entity_label <> '')", victim),
			"%s's retained journal still names the victim's people after their erasure", schema)

		// The bystander is the control for both: a redaction with no tenant predicate,
		// and a sweep that took everything, each satisfy the two assertions above.
		assert.Equalf(t, int64(1), count(t, db, tbl,
			"tenant_id = ? AND actor <> '' AND entity_label <> ''", bystander),
			"%s redacted a tenant nobody deleted", schema)
	}

	// The victim is gone everywhere.
	for _, c := range tenantRowCounts() {
		assert.Zerof(t, count(t, db, c.table, c.where, victim), "%s still holds the victim", c.label)
	}
	assert.Zerof(t, count(t, db, rollupMat, "tenant_id = ?", victim),
		"%s still holds the victim's aggregate buckets. The raw measurements are gone, so every "+
			"reading through the aggregate's own view reports clean while the copy survives — and "+
			"the refresh policy's trailing window would never revisit a bucket to remove it",
		rollupMat)
	assert.Zero(t, count(t, db, `"user-management".iam_membership_tenant_roles`,
		"membership_id = ?", 1005),
		"the many2many join row carries nothing naming a tenant; only the foreign key reaches it")

	// The bystander is untouched. This is the control: a sweep with no predicate at all
	// would satisfy every assertion above.
	for _, c := range tenantRowCounts() {
		assert.Equalf(t, c.rows, count(t, db, c.table, c.where, bystander),
			"%s lost the bystander's rows — the sweep is not tenant-scoped", c.label)
	}
	assert.Equalf(t, bystanderRollupBefore, count(t, db, rollupMat, "tenant_id = ?", bystander),
		"%s lost the bystander's aggregate buckets: the delete against the materialization is not "+
			"tenant-scoped, which is the failure mode a full-range refresh would also have had",
		rollupMat)
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
	assert.Zerof(t, clean.Retained(), "the re-verify found a retained row still naming someone: %+v",
		clean.Redacted)
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

// TestSchemaExistenceIsAnsweredByTheRealCatalog pins the query the telemetry store bets
// its "nothing to erase" conclusion on.
//
// 🔴 GET THE CATALOG OR COLUMN NAME WRONG AND IT ANSWERS "no schema" FOREVER — which that
// store reads as "this instance runs no event-management", i.e. reports clean without
// erasing anything, on every pass, for every tenant. A unit test cannot catch that: any
// stand-in table answers whatever it was built to answer. Only a real database with the
// area's migrations applied can, and this is the one test that has one.
//
// Both directions are asserted, because a query that always returned true would be just as
// wrong in the other direction — it would make an instance without the area block forever.
func TestSchemaExistenceIsAnsweredByTheRealCatalog(t *testing.T) {
	db, ctx := drillConn(t)

	for _, area := range areas {
		found, err := tenantpurge.SchemaExists(ctx, db, area.name)
		require.NoErrorf(t, err, "probing for %s", area.name)
		assert.Truef(t, found, "%s has been migrated into this database, so the probe the "+
			"telemetry store trusts must see its schema — if it cannot, that store reports "+
			"clean without erasing anything", area.name)
	}

	absent, err := tenantpurge.SchemaExists(ctx, db, "no-such-functional-area")
	require.NoError(t, err)
	assert.False(t, absent,
		"a probe that answers yes for everything makes an instance without the area block forever")
}

// TestTheDetectPartitionQueryRunsOnTheRealSchema pins the statement the ADR-077 `detect`
// store bets its whole coverage claim on: the list of DETECT partitions that owe an answer.
//
// 🔴 GET THE TABLE OR COLUMN NAME WRONG AND THE STORE REPORTS CLEAN WITHOUT ASKING ANYONE.
// An empty partition list means "no partition has durable state", which — with no engine
// subscribed — is the store's one clean-and-say-nothing path. A renamed column would not
// even produce that: it errors, which is loud and retryable. A renamed TABLE that still
// exists under the old name, or a schema-qualification that quotes wrongly, is the shape
// that goes quiet.
//
// The store's own unit tests cannot catch any of it. They coax SQLite into answering the
// same statement by ATTACHing a database named for the schema — which is the right way to
// exercise the statement verbatim, and is exactly why it agrees with whatever this codebase
// currently believes the shape to be. Only a database with event-processing's migrations
// actually applied can say whether that belief is true, and this is the one test that has
// one.
//
// 🔴 THE QUERY IS IMPORTED, NOT COPIED, and an earlier draft got that wrong on a
// justification that does not survive checking: it said importing the owning package "would
// invert the dependency for one string". This module's go.mod already requires
// dc-user-management, and registry.go already imports both dc-user-management/schema and
// dc-event-processing/model — there is no dependency to invert. A copy would make the only
// test that proves the statement runs against a real schema prove it about a DIFFERENT
// string, kept in step by a comment.
func TestTheDetectPartitionQueryRunsOnTheRealSchema(t *testing.T) {
	db, _ := drillConn(t)

	query := tenantpurge.DetectPartitionsQuery
	var ids []string
	require.NoErrorf(t, db.Raw(query).Scan(&ids).Error, "the DETECT partition query does not run "+
		"against a real migrated schema: %s", query)

	// 🔑 THE NON-VACUITY CONTROL. A migrated-but-empty table returns no rows, which is
	// indistinguishable from a query that reads the wrong place and finds nothing. Seed a
	// partition and require the statement to see it, so "no rows" can only ever mean the
	// table is empty.
	require.NoError(t, db.Exec(`INSERT INTO "event-processing"."detect_snapshots" `+
		`(partition_id, stream_seq, watermark, payload, updated_at) `+
		`VALUES ('drill-partition', 1, now(), '{}'::bytea, now()) `+
		`ON CONFLICT (partition_id) DO NOTHING`).Error)
	require.NoError(t, db.Raw(query).Scan(&ids).Error)
	require.Contains(t, ids, "drill-partition",
		"the query ran but did not return a row that is demonstrably in the table, so it is "+
			"reading something other than the DETECT checkpoint")
}

// TestTheFenceExistsInEveryAreaAndIsPlantedAndLifted drives the ADR-077 erasure fence
// against a database with every area migrated into it.
//
// 🔴 THIS IS THE ONLY PLACE THE FENCE'S CENTRAL CLAIM CAN BE CHECKED. The unit tests
// prove the callback refuses a write when a row stands; they cannot prove the row can be
// stood in the first place, because that depends on core having created purged_tenants in
// EVERY functional area's schema. An area missing the table would leave its writes
// unfenced while every unit test stayed green, and the coordinator's fail-closed
// derivation would only discover it during a real customer's deletion.
//
// It also pins the direction the plant and the lift move: a planted fence has a null
// completed_at (it stands), and a lifted one does not (a successor at the released token
// can write). Asserting only that the rows exist would pass with the two swapped.
func TestTheFenceExistsInEveryAreaAndIsPlantedAndLifted(t *testing.T) {
	db, ctx := drillConn(t)

	plan, err := tenantpurge.Classify(ctx, db)
	require.NoError(t, err)

	const fenced = "fence-drill-tenant"
	epoch := time.Now().UTC().Add(-time.Hour)

	// 🔴 START FROM A KNOWN-EMPTY STATE, IN EVERY SCHEMA. A first draft cleaned up only
	// user-management and only at the end, which left a standing fence in the other nine
	// and a fresh epoch each run — so the second run of this test saw three rows where it
	// asserts one, and the drill went red on a defect that was entirely the test's. The
	// preamble of the sweep test above exists for the same reason.
	clearFences := func() {
		for _, a := range areas {
			mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.%s WHERE token = ?`, a.name, rdb.FenceTable), fenced)
		}
	}
	clearFences()
	t.Cleanup(clearFences)

	schemas, err := tenantpurge.PlantFence(ctx, db, plan, fenced, epoch, time.Now().UTC(), nil)
	require.NoError(t, err, "the fence could not be planted against a real migrated database")

	// The area set is derived from the plan, so assert it against the areas this harness
	// knows it migrated rather than against itself.
	require.Len(t, schemas, len(areas),
		"the fence was planted in %d schema(s) but this database holds %d functional areas — "+
			"an unfenced area is one whose writes keep being accepted after it acks swept",
		len(schemas), len(areas))
	for _, a := range areas {
		assert.Containsf(t, schemas, a.name, "%s was not fenced", a.name)
	}

	standing := func(schema string) (total, open int64) {
		require.NoError(t, db.Raw(fmt.Sprintf(
			`SELECT count(*), count(*) FILTER (WHERE completed_at IS NULL) FROM %q.%s WHERE token = ?`,
			schema, rdb.FenceTable), fenced).Row().Scan(&total, &open))
		return
	}

	for _, a := range areas {
		total, open := standing(a.name)
		assert.Equalf(t, int64(1), total, "%s carries %d fence rows, want exactly one", a.name, total)
		assert.Equalf(t, int64(1), open, "%s's fence is not standing, so writes for a purged "+
			"tenant would be admitted there while the sweep ran", a.name)
	}

	// Planting is idempotent: every pass plants, and a second pass must not double the row.
	_, err = tenantpurge.PlantFence(ctx, db, plan, fenced, epoch, time.Now().UTC(), nil)
	require.NoError(t, err, "planting is not idempotent, so every pass after the first errors")
	for _, a := range areas {
		total, open := standing(a.name)
		assert.Equalf(t, int64(1), total, "%s gained a second row from the second plant", a.name)
		assert.Equalf(t, int64(1), open, "%s's fence closed on a re-plant", a.name)
	}

	require.NoError(t, tenantpurge.LiftFence(ctx, db, plan, fenced, time.Now().UTC(), nil))
	for _, a := range areas {
		total, open := standing(a.name)
		assert.Equalf(t, int64(1), total, "%s gained a row from the lift", a.name)
		assert.Zerof(t, open, "%s's fence is still standing after completion, so a successor "+
			"tenant at the released token could never write and nothing would say why", a.name)
	}

	// 🔴 A LIFTED FENCE IS RE-OPENED BY THE NEXT PLANT. Lifting happens immediately before
	// the token is released, so anything that fails in between leaves a purge running with
	// its fences down — and a plant that treated the completed row as "already there"
	// would leave that area unfenced for every remaining pass.
	_, err = tenantpurge.PlantFence(ctx, db, plan, fenced, epoch, time.Now().UTC(), nil)
	require.NoError(t, err)
	for _, a := range areas {
		_, open := standing(a.name)
		assert.Equalf(t, int64(1), open, "%s's fence was not re-opened after a lift, so a purge "+
			"that failed between lifting and completing would run on unfenced areas", a.name)
	}
}

// drillDatabase adapts the drill's gorm handle to the purge store's Database interface.
// The handle is already in whatever context the store hands it; the drill has no
// per-request scope of its own.
type drillDatabase struct{ db *gorm.DB }

func (d drillDatabase) DB(ctx context.Context) *gorm.DB { return d.db.WithContext(ctx) }

// TestTheRelationalStorePlantsItsFenceWhenItSweeps closes the one hole the fence's own
// tests leave open.
//
// 🔴 A MUTATION FOUND THIS: deleting the PlantFence call from Relational.Erase left every
// test in the tree green. The direct test above proves the fence CAN be planted; nothing
// proved the store that sweeps a tenant's rows is the thing that plants it. That is the
// wiring, and it is the half that can be removed by accident — the mechanism keeps
// working, in a package nobody calls, while every purge acks "swept" over an area whose
// writes were never stopped.
//
// It sweeps a tenant with no rows on purpose. What is under test is not the delete — the
// test above this one covers that exhaustively — it is that the pass plants before it
// sweeps, which has to hold on a pass that finds nothing as much as on one that does.
func TestTheRelationalStorePlantsItsFenceWhenItSweeps(t *testing.T) {
	db, ctx := drillConn(t)

	const fenced = "fence-wiring-tenant"
	clear := func() {
		for _, a := range areas {
			mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.%s WHERE token = ?`, a.name, rdb.FenceTable), fenced)
		}
	}
	clear()
	t.Cleanup(clear)

	// No precondition: the interlock is user-management's, and iam_tenants holds no row
	// for this token. What is under test is the plant, not the guard on it.
	store := purge.NewRelational("rdb", drillDatabase{db}, nil)
	if _, err := store.Erase(ctx, fenced, time.Now().UTC().Add(-time.Hour)); err != nil {
		require.NoError(t, err)
	}

	for _, a := range areas {
		var open int64
		require.NoError(t, db.Raw(fmt.Sprintf(
			`SELECT count(*) FROM %q.%s WHERE token = ? AND completed_at IS NULL`,
			a.name, rdb.FenceTable), fenced).Row().Scan(&open))
		assert.Equalf(t, int64(1), open, "the relational store swept %s without fencing it, so "+
			"its writes for this tenant were never stopped while the sweep ran", a.name)
	}

	// 🔴 AND BOTH ENDS CARRY THE PRECONDITION, which a mutation showed nothing pinned. A
	// lift misdirected at a live token takes down the fence protecting a purge that is
	// still running; the guard that stops it is a parameter, and a parameter passed as nil
	// looks exactly like one passed correctly at every call site.
	refused := errors.New("that token names a live tenant")
	guarded := purge.NewRelational("rdb", drillDatabase{db},
		func(string) tenantpurge.Precondition { return func(*gorm.DB) error { return refused } })
	require.ErrorIs(t, guarded.LiftFence(ctx, fenced, time.Now().UTC()), refused,
		"the store lifted a fence over a refusing precondition")
	if _, err := guarded.Erase(ctx, fenced, time.Now().UTC()); !errors.Is(err, refused) {
		t.Fatalf("the store planted over a refusing precondition: %v", err)
	}

	// And the store lifts what it planted, through the same wiring.
	require.NoError(t, store.LiftFence(ctx, fenced, time.Now().UTC()))
	for _, a := range areas {
		var open int64
		require.NoError(t, db.Raw(fmt.Sprintf(
			`SELECT count(*) FROM %q.%s WHERE token = ? AND completed_at IS NULL`,
			a.name, rdb.FenceTable), fenced).Row().Scan(&open))
		assert.Zerof(t, open, "%s's fence outlived the store's own lift", a.name)
	}
}

// TestTheSweepRetainsAndRedactsTheJournalThroughTheStore drives the retention through
// the store that performs it, on a real migrated schema.
//
// The store's REFUSAL when a retained row still names someone is not driven here, and
// that is a property of the mechanism rather than a gap left open: a row still carrying
// an identifier at scan time is one that landed between the redaction and the read, and a
// pass that runs both back to back cannot be made to produce one. The scan is exercised
// directly in core/tenantpurge and the decision in purge.retainedError.
func TestTheSweepRetainsAndRedactsTheJournalThroughTheStore(t *testing.T) {
	db, ctx := drillConn(t)

	const tenant = "retention-drill-tenant"
	const bystander = "retention-drill-bystander"
	clear := func() {
		for _, a := range areas {
			for _, tok := range []string{tenant, bystander} {
				mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.%s WHERE token = ?`, a.name, rdb.FenceTable), tok)
				mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.audit_events WHERE tenant_id = ?`, a.name), tok)
			}
		}
	}
	clear()
	t.Cleanup(clear)

	for _, tok := range []string{tenant, bystander} {
		mustExec(t, db, `INSERT INTO "user-management".audit_events
		       (occurred_time, tenant_id, category, actor, operation, entity_label, rows_affected)
		     VALUES (now(), ?, 'mutation', ?, 'create', ?, 1)`, tok, tok+"@example.test", tok+"-jane")
	}

	store := purge.NewRelational("rdb", drillDatabase{db}, nil)
	if _, err := store.Erase(ctx, tenant, time.Now().UTC().Add(-time.Hour)); err != nil {
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1),
		count(t, db, `"user-management".audit_events`, "tenant_id = ?", tenant),
		"the journal row was swept, so the erasure has no evidence")
	assert.Zero(t, count(t, db, `"user-management".audit_events`,
		"tenant_id = ? AND (actor <> '' OR entity_label <> '')", tenant),
		"the retained journal still names the erased tenant's people")

	// The control: a redaction with no tenant predicate satisfies both assertions above.
	assert.Equal(t, int64(1), count(t, db, `"user-management".audit_events`,
		"tenant_id = ? AND actor <> '' AND entity_label <> ''", bystander),
		"the store redacted a tenant nobody deleted")
}

// TestTheStoreRefusesToAckWhenTheRedactionDidNotTake drives the store's own reading of
// the retained scan, on a real migrated schema.
//
// 🔴 IT NEEDS A TRIGGER, AND THAT IS THE POINT. The store sweeps and then scans in one
// pass, so a straggler inserted beforehand is simply redacted by the pass that would have
// caught it — which is why the branch had no test at all and a mutation disabling it
// survived. A BEFORE UPDATE trigger that puts the old value back makes the redaction a
// no-op, so the scan finds exactly what it exists to find: a retained row that still names
// someone after the erasure the deletion record is about to certify.
func TestTheStoreRefusesToAckWhenTheRedactionDidNotTake(t *testing.T) {
	db, ctx := drillConn(t)

	const tenant = "redaction-failure-tenant"
	clear := func() {
		for _, a := range areas {
			mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.%s WHERE token = ?`, a.name, rdb.FenceTable), tenant)
			mustExec(t, db, fmt.Sprintf(`DELETE FROM %q.audit_events WHERE tenant_id = ?`, a.name), tenant)
		}
	}
	clear()
	mustExec(t, db, `DROP TRIGGER IF EXISTS dc_drill_block_redaction ON "user-management".audit_events`)
	t.Cleanup(func() {
		mustExec(t, db, `DROP TRIGGER IF EXISTS dc_drill_block_redaction ON "user-management".audit_events`)
		clear()
	})

	mustExec(t, db, `INSERT INTO "user-management".audit_events
	       (occurred_time, tenant_id, category, actor, operation, entity_label, rows_affected)
	     VALUES (now(), ?, 'mutation', 'jane@example.test', 'create', 'jane-doe', 1)`, tenant)

	mustExec(t, db, `CREATE OR REPLACE FUNCTION dc_drill_keep_actor() RETURNS trigger AS $$
	    BEGIN NEW.actor := OLD.actor; RETURN NEW; END; $$ LANGUAGE plpgsql`)
	mustExec(t, db, `CREATE TRIGGER dc_drill_block_redaction BEFORE UPDATE
	    ON "user-management".audit_events FOR EACH ROW EXECUTE FUNCTION dc_drill_keep_actor()`)

	store := purge.NewRelational("rdb", drillDatabase{db}, nil)
	_, err := store.Erase(ctx, tenant, time.Now().UTC().Add(-time.Hour))
	require.Error(t, err, "the store acked while a retained row still named the erased tenant's people")
	assert.Contains(t, err.Error(), "still name someone")
	assert.NotContains(t, err.Error(), "still writing",
		"the refusal reads as a failed delete, which sends an operator looking for a service to stop")

	// The control: with the trigger gone, the identical pass acks. Without it, a store
	// that refused everything would satisfy the assertion above.
	mustExec(t, db, `DROP TRIGGER dc_drill_block_redaction ON "user-management".audit_events`)
	_, err = store.Erase(ctx, tenant, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err, "the store refuses a pass whose redaction did take")
}
