// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the governed, read-only SQL/BI surface, against a REAL
// TimescaleDB and over a REAL Postgres connection made AS the read role.
//
// 🔴 WHY THESE CANNOT BE UNIT TESTS, AND WHY NO SCHEMA DIFF SUBSTITUTES FOR THEM.
//
// The property under test is "a BI tool connected over the Postgres wire cannot read
// another tenant's rows". Every mechanism that decides it is outside Go: the grant an
// analytics role does or does not hold, the way PostgreSQL expands a view as its owner,
// and the value of current_user in the session. sqlite has none of them.
//
// The other gate cannot see it either, and that is the sharper point. hack/migration-diff.sh
// runs `pg_dump --schema-only --no-privileges`, so an ACL is stripped from the comparison
// entirely: REVOKE the surface's SELECT, or GRANT a reader the run of the area schema,
// and the golden does not move and `verify` reports every area green. The grant boundary
// has exactly one instrument, and it is this file.
//
// Every check here is paired with a negative control, because a boundary nobody has seen
// fail is not a boundary. Each control is applied, measured, and reverted in place.
//
// Run it with:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=devicechain -P \
//	  timescale/timescaledb:latest-pg16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration ./model/... -run IntegrationAnalytics -v
//
// The harness connects as a SUPERUSER (DC_IT_PGUSER, default postgres) because it has to
// create roles, which the platform's own application role deliberately cannot do.
package model

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

const (
	analyticsITInstance = "dcanalyticsit"
	analyticsITPassword = "analytics-it-password"
	tenantA             = "acme"
	tenantB             = "bravo"
)

// analyticsITRoles is every role this suite creates. Named in one place so setup and
// teardown cannot disagree — a leftover role would make the NEXT run's CREATE fail with
// a message about the role rather than about the test.
var analyticsITRoles = []string{
	AnalyticsReaderRole,
	AnalyticsRolePrefix + tenantA,
	AnalyticsRolePrefix + tenantB,
	// A member of the group whose name carries no tenant. It exists to prove the
	// fail-closed direction: reader_tenant() returns NULL, so it reads nothing rather
	// than everything.
	"bi_tool_without_a_tenant",
}

// analyticsHarness migrates a fresh instance, seeds two tenants into every hypertable,
// creates the roles, and reconciles the grants — i.e. it stands up exactly what an
// operator would have after following the documented steps.
func analyticsHarness(t *testing.T) (*rdb.RdbManager, *gorm.DB) {
	t.Helper()
	mgr := newPostgresManager(t, analyticsITInstance)
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))

	for _, table := range []string{
		"events", "location_events", "measurement_events",
		"alert_events", "event_anchors", "state_change_events",
	} {
		require.NoErrorf(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "event-management".%q`, table)).Error,
			"truncate %s", table)
	}
	seedBothTenants(t, sys)
	createAnalyticsITRoles(t, sys)
	require.NoError(t, ReconcileAnalyticsSurface(context.Background(), mgr))
	return mgr, sys
}

// seedBothTenants writes one row per hypertable for each of two tenants, through raw SQL
// rather than the Api: the subject here is what a reader can SEE, and going around the
// tenant-scope callback is the whole point — it proves the rows are there for both
// tenants before any reader looks.
func seedBothTenants(t *testing.T, sys *gorm.DB) {
	t.Helper()
	occurred := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	for _, tenant := range []string{tenantA, tenantB} {
		id := []byte(tenant + "-event-id")
		pid := []byte(tenant + "-payload-id")
		stmts := []struct {
			sql  string
			args []interface{}
		}{
			{`INSERT INTO "event-management"."events"
			  (tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
			  VALUES (?, ?, ?, 1, ?, 'test', ?)`, []interface{}{tenant, id, tenant + "-device", occurred, occurred}},
			{`INSERT INTO "event-management"."location_events"
			  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, latitude, longitude)
			  VALUES (?, ?, ?, ?, 1, ?, 1.0, 2.0)`, []interface{}{tenant, id, pid, tenant + "-device", occurred}},
			// 🔴 A DISTINCT VALUE PER TENANT, and it is load-bearing rather than
			// cosmetic. The security-barrier test singles out the OTHER tenant's row
			// with an arithmetic qual, which it cannot do if both rows hold the same
			// number — the control would then be indistinguishable from a no-op.
			{`INSERT INTO "event-management"."measurement_events"
			  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value)
			  VALUES (?, ?, ?, ?, 1, ?, 'temp', ?)`, []interface{}{tenant, id, pid, tenant + "-device", occurred, measurementValue(tenant)}},
			{`INSERT INTO "event-management"."alert_events"
			  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, type, level, message, source)
			  VALUES (?, ?, ?, ?, 1, ?, 'k', 1, 'm', 'test')`, []interface{}{tenant, id, pid, tenant + "-device", occurred}},
			{`INSERT INTO "event-management"."event_anchors"
			  (tenant_id, event_id, device_token, event_type, occurred_time, anchor_type, anchor_token)
			  VALUES (?, ?, ?, 1, ?, 'asset', ?)`, []interface{}{tenant, id, tenant + "-device", occurred, tenant + "-anchor"}},
			{`INSERT INTO "event-management"."state_change_events"
			  (tenant_id, event_id, device_token, event_type, occurred_time, state, reason, session_id)
			  VALUES (?, ?, ?, 1, ?, 'online', 'connect', 1)`, []interface{}{tenant, id, tenant + "-device", occurred}},
		}
		for _, s := range stmts {
			require.NoErrorf(t, sys.Exec(s.sql, s.args...).Error, "seed %s", tenant)
		}
	}
	// The rollup is a real-time continuous aggregate (materialized_only = false), so it
	// unions the un-materialized leading edge and these rows are visible through it
	// without a refresh. Refreshing anyway would make the test depend on the policy job.
}

// measurementValue is the reading seeded for a tenant. The two must differ; see the
// comment at the insert.
func measurementValue(tenant string) float64 {
	if tenant == tenantA {
		return 21.5
	}
	return 42.0
}

func createAnalyticsITRoles(t *testing.T, sys *gorm.DB) {
	t.Helper()
	dropAnalyticsITRoles(t, sys)
	require.NoError(t, sys.Exec(fmt.Sprintf("CREATE ROLE %q NOLOGIN", AnalyticsReaderRole)).Error)
	for _, role := range analyticsITRoles {
		if role == AnalyticsReaderRole {
			continue
		}
		require.NoErrorf(t, sys.Exec(fmt.Sprintf(
			"CREATE ROLE %q LOGIN PASSWORD '%s' CONNECTION LIMIT 4 IN ROLE %q",
			role, analyticsITPassword, AnalyticsReaderRole)).Error, "create %s", role)
	}
	t.Cleanup(func() { dropAnalyticsITRoles(t, sys) })
}

func dropAnalyticsITRoles(t *testing.T, sys *gorm.DB) {
	t.Helper()
	for _, role := range analyticsITRoles {
		// DROP OWNED BY first: a role holding a privilege in this database cannot be
		// dropped, and the failure names a dependency rather than the grant.
		_ = sys.Exec(fmt.Sprintf("DROP OWNED BY %q", role)).Error
		_ = sys.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %q", role)).Error
	}
}

// connectAs opens a real Postgres connection as one of the reader roles.
func connectAs(t *testing.T, role string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), analyticsDSN(t, role))
	require.NoErrorf(t, err, "connect as %s", role)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func analyticsDSN(t *testing.T, role string) string {
	t.Helper()
	port, err := strconv.Atoi(envOr("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		role, analyticsITPassword, envOr("DC_IT_PGHOST", "localhost"), port, analyticsITInstance)
}

// tenantsVisible returns the distinct tenant_id values a connection can see through one
// analytics view, with their row counts.
func tenantsVisible(t *testing.T, conn *pgx.Conn, view string) map[string]int {
	t.Helper()
	rows, err := conn.Query(context.Background(), fmt.Sprintf(
		`SELECT tenant_id, count(*) FROM %q.%q GROUP BY 1`, AnalyticsSchema, view))
	require.NoErrorf(t, err, "read %s", view)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var tenant string
		var n int
		require.NoError(t, rows.Scan(&tenant, &n))
		out[tenant] = n
	}
	require.NoError(t, rows.Err())
	return out
}

// TestIntegrationAnalyticsReaderSeesOnlyItsOwnTenant is the acceptance test for the whole
// item, run where it can actually fail: a real Postgres connection, made by a real read
// role, against every relation on the surface.
//
// The negative control is inside it, and it is the one that matters: the same query is
// re-run against a view whose tenant predicate has been removed. Both tenants come back.
// Without that step, "the reader saw one tenant" is equally consistent with a harness
// that only ever seeded one.
func TestIntegrationAnalyticsReaderSeesOnlyItsOwnTenant(t *testing.T) {
	_, sys := analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)

	for _, view := range AnalyticsViewNames() {
		visible := tenantsVisible(t, conn, view)
		require.Equalf(t, 1, len(visible),
			"%s exposed %d tenants to a reader for %q: %v", view, len(visible), tenantA, visible)
		require.Containsf(t, visible, tenantA, "%s hid the reader's OWN rows: %v", view, visible)
		require.Positivef(t, visible[tenantA], "%s returned no rows, so it proves nothing", view)
	}

	// 🔴 NEGATIVE CONTROL. Rebuild one view without its tenant predicate and re-read it.
	// The reader must now see BOTH tenants — which is what proves the seeding put another
	// tenant's rows within reach, and that the predicate is the only thing holding them back.
	const probe = "measurement_events"
	require.NoError(t, sys.Exec(fmt.Sprintf(
		`CREATE OR REPLACE VIEW %q.%q WITH (security_barrier = true) AS
		 SELECT tenant_id, event_id, payload_id, device_token, event_type, occurred_time,
		        name, value, classifier, unit, data_type
		 FROM %q.%q`,
		AnalyticsSchema, probe, AnalyticsAreaSchema, probe)).Error)

	leaked := tenantsVisible(t, conn, probe)
	require.Lenf(t, leaked, 2,
		"the negative control did not leak, so the positive result above proves nothing: %v", leaked)
	require.Contains(t, leaked, tenantB)

	// Put it back, through the same code the migration runs.
	require.NoError(t, execAnalyticsSurface(sys))
	restored := tenantsVisible(t, conn, probe)
	require.Len(t, restored, 1, "the surface did not recover: %v", restored)
}

// TestIntegrationAnalyticsReaderWithNoTenantReadsNothing pins the fail-closed direction.
//
// A role that is a member of the reader group but whose name carries no tenant resolves
// to NULL, and `tenant_id = NULL` is never true. The alternative — a role that is not
// recognised reading EVERYTHING — is the failure mode this design exists to avoid, and it
// is one line of SQL away (a COALESCE in reader_tenant, an OR in a view).
func TestIntegrationAnalyticsReaderWithNoTenantReadsNothing(t *testing.T) {
	analyticsHarness(t)
	conn := connectAs(t, "bi_tool_without_a_tenant")

	var tenant *string
	require.NoError(t, conn.QueryRow(context.Background(),
		fmt.Sprintf("SELECT %q.reader_tenant()", AnalyticsSchema)).Scan(&tenant))
	require.Nil(t, tenant, "a role outside the naming convention resolved to a tenant")

	for _, view := range AnalyticsViewNames() {
		require.Emptyf(t, tenantsVisible(t, conn, view),
			"%s returned rows to a role with no tenant identity", view)
	}
}

// TestIntegrationAnalyticsReaderCannotWrite pins the "read-only" half of the claim.
//
// Read-only here is a GRANT, not a setting: the role holds SELECT and nothing else, so
// there is no privilege to exercise. That matters because the obvious alternative —
// `ALTER ROLE ... SET default_transaction_read_only = on` — is USERSET and the reader can
// simply turn it off. Only the grant binds.
func TestIntegrationAnalyticsReaderCannotWrite(t *testing.T) {
	analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)
	ctx := context.Background()

	writes := []string{
		fmt.Sprintf(`INSERT INTO %q."measurement_events" (tenant_id, event_id, payload_id, device_token,
		   event_type, occurred_time, name, value) VALUES ('%s','a','b','c',1,now(),'x',1)`,
			AnalyticsSchema, tenantA),
		fmt.Sprintf(`UPDATE %q."measurement_events" SET value = 0`, AnalyticsSchema),
		fmt.Sprintf(`DELETE FROM %q."measurement_events"`, AnalyticsSchema),
		fmt.Sprintf(`CREATE TABLE %q."scratch" (x int)`, AnalyticsSchema),
	}
	for _, stmt := range writes {
		_, err := conn.Exec(ctx, stmt)
		require.Errorf(t, err, "a read role executed a write: %s", stmt)
	}

	// The read role must also be unable to turn itself into a writer by resetting a
	// session default.
	_, err := conn.Exec(ctx, "SET default_transaction_read_only = off")
	require.NoError(t, err, "the setting is USERSET, which is exactly why it is not the mechanism")
	_, err = conn.Exec(ctx, fmt.Sprintf(`DELETE FROM %q."measurement_events"`, AnalyticsSchema))
	require.Error(t, err, "the write succeeded once the session setting was cleared")
}

// TestIntegrationAnalyticsGrantsConvergeOnEveryBoot is the negative control for the ONE
// backstop this surface has.
//
// Row-level security is unavailable here (TimescaleDB refuses to combine it with
// compression in either order — measured, see analytics.go), so nothing under the views
// constrains a reader that is handed direct access to the hypertables. The design's
// answer is that ReconcileAnalyticsSurface REMOVES such access on every boot rather than
// merely never granting it. This test grants it, proves the leak is real, then boots.
func TestIntegrationAnalyticsGrantsConvergeOnEveryBoot(t *testing.T) {
	mgr, sys := analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)
	ctx := context.Background()

	crossTenant := fmt.Sprintf(`SELECT count(DISTINCT tenant_id) FROM %q."measurement_events"`,
		AnalyticsAreaSchema)

	// Before: the area schema is unreachable, so the query fails on permissions.
	var n int
	require.Error(t, conn.QueryRow(ctx, crossTenant).Scan(&n),
		"the read role could already reach the area schema")

	// 🔴 THE CONTROL. Hand the group role exactly what a careless `GRANT ... ON ALL
	// TABLES` would, and confirm every tenant is now readable.
	require.NoError(t, sys.Exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA %q TO %q`,
		AnalyticsAreaSchema, AnalyticsReaderRole)).Error)
	require.NoError(t, sys.Exec(fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA %q TO %q`,
		AnalyticsAreaSchema, AnalyticsReaderRole)).Error)

	require.NoError(t, conn.QueryRow(ctx, crossTenant).Scan(&n))
	require.Equalf(t, 2, n,
		"the control did not leak, so the convergence below would prove nothing (saw %d tenants)", n)

	// Boot. The reconciler must take it back.
	require.NoError(t, ReconcileAnalyticsSurface(ctx, mgr))
	require.Error(t, conn.QueryRow(ctx, crossTenant).Scan(&n),
		"a boot left the read role holding privileges on the area schema")

	// 🔴 AND THE PRIVILEGE ITSELF IS GONE, not merely one query's worth of it. Asserting
	// that a SELECT fails is weaker than it reads: USAGE on the schema without SELECT on
	// the tables fails that query too, so a boot that granted schema access and no table
	// access would satisfy the line above while leaving the reader one GRANT away from
	// every tenant. Ask the catalog what the role holds.
	var usage bool
	require.NoError(t, sys.Raw(`SELECT has_schema_privilege(?, ?, 'USAGE')`,
		AnalyticsReaderRole, AnalyticsAreaSchema).Scan(&usage).Error)
	require.Falsef(t, usage, "the read role still holds USAGE on %q after a boot", AnalyticsAreaSchema)

	// And the surface itself must still work afterwards — a revoke that also took the
	// views away would pass the lines above while breaking the feature.
	require.Len(t, tenantsVisible(t, conn, "measurement_events"), 1)
}

// TestIntegrationAnalyticsConnectionLimitBinds is the governance leg, measured rather
// than asserted.
//
// 🔴 It exists because the OTHER half of the governance story does NOT bind, and the two
// look identical when written down side by side. `ALTER ROLE ... SET statement_timeout` is
// USERSET: a reader raises it in its own session with one statement, so it is a sensible
// default and never a ceiling — which is why the platform declares no query-time limit and
// the documentation says an operator may set one knowing what it is worth. CONNECTION LIMIT
// is enforced at authentication, where the client has no say.
//
// The statement_timeout half is applied by this test rather than by the platform, precisely
// so the claim that it does not bind is measured rather than asserted.
func TestIntegrationAnalyticsConnectionLimitBinds(t *testing.T) {
	_, sys := analyticsHarness(t)
	role := AnalyticsRolePrefix + tenantB
	ctx := context.Background()

	require.NoError(t, sys.Exec(fmt.Sprintf("ALTER ROLE %q CONNECTION LIMIT 1", role)).Error)
	require.NoError(t, sys.Exec(fmt.Sprintf("ALTER ROLE %q SET statement_timeout = '5s'", role)).Error)

	held, err := pgx.Connect(ctx, analyticsDSN(t, role))
	require.NoError(t, err, "the first connection must succeed, or the refusal below means nothing")
	defer func() { _ = held.Close(ctx) }()

	_, err = pgx.Connect(ctx, analyticsDSN(t, role))
	require.Error(t, err, "a second connection was accepted over a CONNECTION LIMIT of 1")
	require.Contains(t, err.Error(), "too many connections")

	// The cap is not a permanent lockout: releasing the connection releases the slot.
	require.NoError(t, held.Close(ctx))
	again, err := pgx.Connect(ctx, analyticsDSN(t, role))
	require.NoError(t, err, "the slot was not returned when the connection closed")
	defer func() { _ = again.Close(ctx) }()

	// 🔴 The counterpart, stated as a test rather than as a comment: the query-time
	// default is advisory. If this ever starts failing, PostgreSQL has changed and the
	// documentation for this surface should be revisited.
	var timeout string
	require.NoError(t, again.QueryRow(ctx, "SHOW statement_timeout").Scan(&timeout))
	require.Equal(t, "5s", timeout, "the role-level default did not apply")
	_, err = again.Exec(ctx, "SET statement_timeout = '1h'")
	require.NoError(t, err)
	require.NoError(t, again.QueryRow(ctx, "SHOW statement_timeout").Scan(&timeout))
	require.Equalf(t, "1h", timeout,
		"statement_timeout resisted being raised by the reader; if PostgreSQL now binds it, "+
			"the docs for this surface understate what is enforced")
}

// TestIntegrationAnalyticsViewsAreSecurityBarriers closes an input class the rest of this
// file did not reach: a leak that returns no rows at all.
//
// 🔴 EVERY OTHER TEST HERE ASKS "WHICH ROWS COME BACK", AND THIS LEAK ANSWERS "NONE".
// Without `security_barrier`, PostgreSQL may evaluate a reader's own WHERE clause BELOW
// the view's tenant predicate — so a non-leakproof expression is applied to rows the
// reader is not allowed to see, and the resulting ERROR is an existence oracle over
// another tenant's data. `1/(value - 42)` raises division by zero if and only if some
// tenant has a reading of exactly 42, and the reader learns that from the error alone.
//
// The row-counting tests are blind to it by construction, which is why dropping the
// barrier survived them. This one reads the error, not the rows.
func TestIntegrationAnalyticsViewsAreSecurityBarriers(t *testing.T) {
	_, sys := analyticsHarness(t)
	ctx := context.Background()

	// The instrument: a cheap, non-leakproof predicate that reports what it was shown.
	// COST 1 is the load-bearing part — the planner orders quals by cost, so a cheap
	// user predicate is the one it will try to evaluate first if it is allowed to.
	require.NoError(t, sys.Exec(fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %q.leak_probe(t text) RETURNS boolean
		  LANGUAGE plpgsql IMMUTABLE COST 1 AS $probe$
		BEGIN
		  IF t <> '%s' THEN RAISE EXCEPTION 'the predicate saw tenant %%', t; END IF;
		  RETURN true;
		END
		$probe$;`, AnalyticsSchema, tenantA)).Error)
	t.Cleanup(func() {
		_ = sys.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %q.leak_probe(text)`, AnalyticsSchema)).Error
	})

	// 🔴 THE READER TURNS OFF INDEX SCANS, AND THAT IS THE ATTACK RATHER THAN A TEST
	// CONVENIENCE. On this schema the tenant predicate normally becomes an index
	// condition, which happens to apply it before any filter — so the leak does not
	// reproduce on the default plan, and a test written against that plan would be
	// asserting a property of the index rather than of the view. The planner GUCs are
	// USERSET: any reader can force the sequential scan for itself, in one statement,
	// which is exactly what makes the barrier necessary rather than decorative.
	forceSeqScan := func(c *pgx.Conn) {
		for _, guc := range []string{"enable_indexscan", "enable_indexonlyscan", "enable_bitmapscan"} {
			_, err := c.Exec(ctx, fmt.Sprintf("SET %s = off", guc))
			require.NoError(t, err)
		}
	}

	conn := connectAs(t, AnalyticsRolePrefix+tenantA)
	forceSeqScan(conn)

	var seen string
	err := conn.QueryRow(ctx, fmt.Sprintf(
		`SELECT tenant_id FROM %q.%q WHERE %q.leak_probe(tenant_id)`,
		AnalyticsSchema, "measurement_events", AnalyticsSchema)).Scan(&seen)
	require.NoError(t, err,
		"the reader's own predicate was evaluated against another tenant's rows")
	require.Equal(t, tenantA, seen)

	// 🔴 THE CONTROL: the same view, the same query, the same plan — with the barrier
	// removed and nothing else changed.
	require.NoError(t, sys.Exec(fmt.Sprintf(
		`CREATE OR REPLACE VIEW %q."barrierless_probe" WITH (security_barrier = false) AS
		 SELECT tenant_id, value FROM %q."measurement_events"
		 WHERE tenant_id = %q.reader_tenant()`,
		AnalyticsSchema, AnalyticsAreaSchema, AnalyticsSchema)).Error)
	require.NoError(t, sys.Exec(fmt.Sprintf(`GRANT SELECT ON %q."barrierless_probe" TO %q`,
		AnalyticsSchema, AnalyticsReaderRole)).Error)
	t.Cleanup(func() {
		_ = sys.Exec(fmt.Sprintf(`DROP VIEW IF EXISTS %q."barrierless_probe"`, AnalyticsSchema)).Error
	})

	_, err = conn.Exec(ctx, fmt.Sprintf(
		`SELECT tenant_id FROM %q."barrierless_probe" WHERE %q.leak_probe(tenant_id)`,
		AnalyticsSchema, AnalyticsSchema))
	require.Error(t, err, "the control did not leak, so the result above proves nothing")
	require.Contains(t, err.Error(), "saw tenant "+tenantB)
}

// TestIntegrationAnalyticsSurfaceHealsADamagedView pins the property that a mutation run
// of this very suite discovered was missing.
//
// 🔴 THE VIEWS ARE THE TENANT BOUNDARY, AND THEY WERE BUILT ONCE. A migration runs once
// and is recorded as done, so a view left unfiltered — by a half-applied replay, by a
// hand-edited definition during an investigation, or (as actually happened here) by a
// mutation harness whose restore step ran the mutated DDL — stayed unfiltered forever.
// Restarting fixed nothing. Only a brand-new database did, which is precisely the
// condition under which nobody notices.
//
// It is worth being exact about how that surfaced, because it is the general shape: the
// leak was invisible while the tests ran against a fresh database and appeared the moment
// they ran against one with a history. A boundary that only holds on a clean install is
// not a boundary.
func TestIntegrationAnalyticsSurfaceHealsADamagedView(t *testing.T) {
	mgr, sys := analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)
	const probe = "measurement_events"

	// Damage it exactly as the accident does: a valid view over the same columns with
	// the tenant predicate gone. Nothing about it looks broken.
	require.NoError(t, sys.Exec(fmt.Sprintf(
		`CREATE OR REPLACE VIEW %q.%q WITH (security_barrier = true) AS
		 SELECT tenant_id, event_id, payload_id, device_token, event_type, occurred_time,
		        name, value, classifier, unit, data_type
		 FROM %q.%q`,
		AnalyticsSchema, probe, AnalyticsAreaSchema, probe)).Error)
	require.Lenf(t, tenantsVisible(t, conn, probe), 2,
		"the damage did not take, so the repair below would prove nothing")

	// A boot repairs it. Note what does NOT: re-running the migration chain, because
	// gormigrate has already recorded this migration as applied.
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	require.Lenf(t, tenantsVisible(t, conn, probe), 2,
		"the migration chain repaired the view, so this test is no longer about the reconciler")

	require.NoError(t, ReconcileAnalyticsSurface(context.Background(), mgr))
	require.Len(t, tenantsVisible(t, conn, probe), 1, "a boot did not restore the tenant boundary")

	// 🔴 THE SECOND DAMAGE SHAPE, because the integrity check now decides whether the
	// rebuild happens at all and it has more than one way to be wrong. A view that keeps
	// its predicate and loses its barrier returns exactly the right ROWS, so every
	// row-counting assertion in this file passes over it. Only the check's own branch
	// sees it.
	require.NoError(t, sys.Exec(fmt.Sprintf(
		`ALTER VIEW %q.%q SET (security_barrier = false)`, AnalyticsSchema, probe)).Error)
	intact, reason, err := analyticsSurfaceIntact(sys)
	require.NoError(t, err)
	require.Falsef(t, intact, "a view with no security barrier was reported intact")
	require.Contains(t, reason, "security barrier")

	require.NoError(t, ReconcileAnalyticsSurface(context.Background(), mgr))
	intact, reason, err = analyticsSurfaceIntact(sys)
	require.NoError(t, err)
	require.Truef(t, intact, "a boot did not restore the security barrier: %s", reason)
}

// TestIntegrationChainRunsAsTheLeastPrivilegeRole closes a gap that predates this change
// and that this change would otherwise have widened.
//
// 🔴 EVERY GATE IN THIS REPO MIGRATES AS A SUPERUSER. hack/migration-diff.sh, the
// integration harness above, and the replay pass all connect as `postgres`. Production
// does not: the platform's role is an unprivileged owner with CREATEDB and pg_monitor and
// nothing else. So a migration that needs one more privilege than that passes every check
// here and crash-loops on the first real boot, with a permissions error naming a statement
// rather than the privilege.
//
// It matters more for this change than for most, because the obvious implementation of a
// governed read role is `CREATE ROLE`, which is exactly the privilege the application does
// not have. This test is what makes "the migration creates no roles" a checked property
// instead of a comment.
//
// The role built here mirrors deploy/opentofu's managed role deliberately, including what
// it LEAVES OUT: no SUPERUSER, no CREATEROLE, no BYPASSRLS.
func TestIntegrationChainRunsAsTheLeastPrivilegeRole(t *testing.T) {
	// A superuser connection, only to mint the unprivileged role the chain then runs as.
	admin := newPostgresManager(t, analyticsITInstance)
	sys := admin.Database.WithContext(core.WithSystemContext(context.Background()))

	const role, password = "dc_leastpriv_it", "least-privilege-it"
	const instance = "dcleastprivit"
	drop := func() {
		_ = sys.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %q", instance)).Error
		_ = sys.Exec(fmt.Sprintf("DROP OWNED BY %q", role)).Error
		_ = sys.Exec(fmt.Sprintf("DROP ROLE IF EXISTS %q", role)).Error
	}
	drop()
	require.NoError(t, sys.Exec(fmt.Sprintf(
		"CREATE ROLE %q LOGIN PASSWORD '%s' CREATEDB", role, password)).Error)
	require.NoError(t, sys.Exec(fmt.Sprintf("GRANT pg_monitor TO %q", role)).Error)
	t.Cleanup(drop)

	port, err := strconv.Atoi(envOr("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": envOr("DC_IT_PGHOST", "localhost"),
				"port":     port,
				"username": role,
				"password": password,
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()),
		"the migration chain needs a privilege the platform's own role does not have")
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})

	// And the read surface is actually there afterwards — a chain that ran without
	// erroring but built nothing would pass the line above.
	own := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	var views int
	require.NoError(t, own.Raw(
		`SELECT count(*) FROM pg_views WHERE schemaname = ?`, AnalyticsSchema).Scan(&views).Error)
	require.Equalf(t, len(AnalyticsViewNames()), views,
		"the unprivileged role built %d of %d analytics views", views, len(AnalyticsViewNames()))

	// The grant reconciler must also survive being run by that role. It grants on objects
	// the role owns and revokes from roles it does not administer, neither of which needs
	// a privilege it holds — but that is an argument, and this is the measurement.
	require.NoError(t, ReconcileAnalyticsSurface(context.Background(), mgr))
}

// TestIntegrationAnalyticsSurfaceIsReRunnable exercises the property the replay gate
// checks structurally, against the live objects: running the migration's DDL a second
// time over its own output must succeed and change nothing observable.
func TestIntegrationAnalyticsSurfaceIsReRunnable(t *testing.T) {
	_, sys := analyticsHarness(t)
	conn := connectAs(t, AnalyticsRolePrefix+tenantA)

	before := tenantsVisible(t, conn, "measurement_events")
	require.NoError(t, execAnalyticsSurface(sys), "the surface DDL is not re-runnable")
	require.NoError(t, execAnalyticsSurface(sys))
	require.Equal(t, before, tenantsVisible(t, conn, "measurement_events"))
}
