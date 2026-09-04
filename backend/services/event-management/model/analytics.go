// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// The governed read-only SQL surface: what it is, and what actually enforces it.
//
// Telemetry lives in TimescaleDB, which speaks the Postgres wire, so anything that
// speaks Postgres/JDBC — Metabase, Grafana, Power BI over the Postgres/ODBC driver —
// can already query it. What did NOT exist was a surface it is safe to point at.
//
// # 🔴 THE FAIL-CLOSED TENANT SCOPE DOES NOT REACH THIS SURFACE
//
// Every tenant-scoped read inside the platform is scoped by a gorm callback
// (backend/core/rdb/tenant_scope.go): it injects `tenant_id = ?` from the request
// context and rejects a tenant-scoped statement that has no tenant. That callback is
// Go. A BI tool connects over the Postgres wire and never runs a line of Go, so the
// callback cannot scope it — not partially, not at all. A read role handed to a BI
// tool with nothing else is a cross-tenant leak with the word "governed" on it.
//
// Isolation for this surface is therefore enforced INSIDE the database, and it is
// made of two things, neither of which is the gorm callback:
//
// (Both are converged on every boot, and "both directions" for the grants means PUBLIC
// as well as every named role — PUBLIC is not in pg_roles, so a reconciler that walks
// that catalog misses the one grantee every login on the instance inherits from.)
//
//  1. **Grants.** The read role holds no privilege whatsoever on the
//     "event-management" schema — not even USAGE — so the hypertables and the
//     continuous aggregate are unreachable by name. It holds SELECT on the views
//     below and nothing else, which is also what makes it read-only: there is no
//     INSERT/UPDATE/DELETE privilege to exercise. ReconcileAnalyticsSurface converges
//     this on every boot, in both directions.
//
//  2. **A tenant predicate compiled into each view**, keyed on `session_user` — the
//     identity the session AUTHENTICATED as. `SET ROLE` cannot move it, and the one
//     statement that could, `SET SESSION AUTHORIZATION`, is refused to a
//     non-superuser. The predicate is `tenant_id = analytics.reader_tenant()`, and
//     reader_tenant() returns NULL for any role that is not an analytics reader — so
//     a role with no analytics identity reads zero rows rather than everything.
//
//     🔴 IT SAID `current_user` HERE, AND THAT SENTENCE WAS FALSE FOR A REASON THIS
//     FILE CREATED. It claimed SET ROLE "needs membership the reader does not have" —
//     but every per-tenant reader is provisioned `IN ROLE analytics_reader`, and
//     membership carries the SET option by default. See createReaderTenantFnStmt for
//     the measurement.
//
// # 🔴 WHY THERE IS NO ROW-LEVEL SECURITY HERE, WHICH IS NOT THE OBVIOUS ANSWER
//
// The obvious design is RLS on the six hypertables, with the views as ergonomics.
// It cannot be built. Measured on BOTH supported majors — PostgreSQL 16, the image the
// goldens are captured against, and PostgreSQL 17, which the deployed operand runs —
// both directions refuse:
//
//	ALTER TABLE <hypertable> SET (timescaledb.compress, ...)   -- on an RLS table
//	  ERROR: columnstore cannot be used on table with row security
//
//	ALTER TABLE <hypertable> ENABLE ROW LEVEL SECURITY          -- on a compressed one
//	  ERROR: operation not supported on hypertables that have columnstore enabled
//
// Compression is not optional here: ApplyDataLifecyclePolicies enables it on exactly
// these six hypertables on every boot. So RLS does not merely fail to help — on a
// FRESH install it applies first and then silently disables compression forever,
// because applyOne logs a failed compression statement and continues by design. The
// symptom would be a telemetry store that grows without bound while every health
// check stays green. RLS was designed in, measured, and removed.
//
// The consequence is that the grant boundary is the ONLY boundary, so it is
// converged rather than assumed: ReconcileAnalyticsSurface revokes, on every boot,
// anything an analytics role has been granted on the area schema.
//
// # WHY security_barrier IS ON EVERY VIEW, ALSO MEASURED
//
// Without it, a reader's own WHERE clause can be pushed BELOW the tenant predicate, so
// a non-leakproof expression is applied to rows the reader may not see and its ERROR
// becomes an existence oracle over another tenant's data.
//
// 🔴 The default plan HIDES this, which is why the test forces the plan the attacker
// would. On this schema the tenant predicate normally becomes an index condition, and a
// leak probe against that plan is really testing the index. The planner GUCs are USERSET:
// a reader runs `SET enable_indexscan = off` for itself and gets the sequential scan
// where cost ordering applies. Measured there — a cheap probe predicate was handed
// another tenant's rows through a barrierless clone of the same view, and was not
// through the real one.

const (
	// AnalyticsSchema is the Postgres schema holding the read surface. It is
	// deliberately NOT the area schema: a reader granted USAGE here still has no
	// route to "event-management", which is what makes the grant boundary a
	// boundary rather than a convention.
	AnalyticsSchema = "analytics"

	// AnalyticsReaderRole is the NOLOGIN group role that owns the surface's
	// privileges. Per-tenant login roles are members of it, so a new reader needs
	// no grant of its own and no service restart.
	//
	// It is created declaratively by the database cluster (a CloudNativePG managed
	// role), not here: the platform's application role holds no CREATEROLE, and
	// giving it CREATEROLE to mint one group role would be a permanent widening of
	// what a compromised pod can do. Its absence is not fatal — the surface simply
	// has no readers — and it is reported rather than assumed.
	AnalyticsReaderRole = "analytics_reader"

	// AnalyticsRolePrefix is the naming convention that carries a reader's tenant.
	// A role named "analytics_acme" reads tenant "acme" and nothing else.
	//
	// 🔴 The tenant is derived from the ROLE NAME rather than from a session
	// setting, and that is the security property. A custom GUC (`SET
	// analytics.tenant = '...'`) is USERSET in PostgreSQL: the reader could set it
	// to another tenant's id in its own session, which turns the whole surface into
	// a cross-tenant read with extra steps. `session_user` is the one identity in
	// the session a client cannot choose — not `current_user`, which SET ROLE moves.
	//
	// 🔴 THE GROUP ROLE'S OWN NAME MATCHES THIS PREFIX, which is why the derivation
	// excludes it explicitly: without that, `analytics_reader` resolves to the tenant
	// `reader`, and `reader` is a legal tenant token under core.ValidateToken. The
	// deployment refuses it as a reader name for the same reason.
	AnalyticsRolePrefix = "analytics_"

	// AnalyticsAreaSchema is the schema the read role must never hold a privilege on.
	AnalyticsAreaSchema = lifecycleSchema

	// analyticsLockName namespaces the boot-time advisory lock that serialises grant
	// reconciliation across concurrently-rolling replicas. Distinct from the
	// migration and lifecycle locks so none of the three ever blocks another.
	analyticsLockName = "event-management-analytics"
)

// analyticsView is one relation exposed on the read surface.
//
// 🔴 THE COLUMN LISTS ARE FROZEN LITERALS AND ARE NOT DERIVED FROM THE LIVE MODELS,
// for the same reason a migration declares its own snapshot structs. `SELECT *` looks
// equivalent and is not: PostgreSQL expands the star ONCE, when the view is created,
// so a fresh install and an upgraded install would end up with different view bodies
// the day a later migration appends a column — the fresh one carrying it, the
// upgraded one not. Naming the columns makes both installs identical and makes
// widening the surface a deliberate, reviewable act.
type analyticsView struct {
	// name is the view's name inside AnalyticsSchema.
	name string
	// source is the relation in the area schema it reads.
	source string
	// columns is the frozen projection, in the source relation's column order.
	columns []string
}

// analyticsViews is the read surface: the six event hypertables plus the
// measurement rollup continuous aggregate (the one relation a BI tool actually
// wants, since it is pre-aggregated and cheap).
//
// The rollup is also the relation that forced this whole design. It is a VIEW owned
// by the platform, and PostgreSQL checks a view's underlying reads as the view's
// OWNER — so no policy or grant on measurement_events could ever have constrained a
// read that arrives through it. Its tenant filter has to be in a view body, which is
// then the uniform mechanism for all seven rather than a special case for one.
var analyticsViews = []analyticsView{
	{
		name:   "events",
		source: "events",
		columns: []string{
			"tenant_id", "event_id", "device_token", "event_type",
			"occurred_time", "source", "alt_id", "processed_time",
		},
	},
	{
		name:   "location_events",
		source: "location_events",
		columns: []string{
			"tenant_id", "event_id", "payload_id", "device_token", "event_type",
			"occurred_time", "latitude", "longitude", "elevation",
			// Appended by NewLocationFixFieldsSchema, which runs before this
			// migration. Named here so the view carries them on a fresh install
			// and an upgraded one alike.
			"accuracy", "speed", "heading",
		},
	},
	{
		name:   "measurement_events",
		source: "measurement_events",
		columns: []string{
			"tenant_id", "event_id", "payload_id", "device_token", "event_type",
			"occurred_time", "name", "value", "classifier", "unit", "data_type",
		},
	},
	{
		name:   "alert_events",
		source: "alert_events",
		columns: []string{
			"tenant_id", "event_id", "payload_id", "device_token", "event_type",
			"occurred_time", "type", "level", "message", "source",
		},
	},
	{
		name:   "event_anchors",
		source: "event_anchors",
		columns: []string{
			"tenant_id", "event_id", "device_token", "event_type",
			"occurred_time", "anchor_type", "anchor_token",
		},
	},
	{
		name:   "state_change_events",
		source: "state_change_events",
		columns: []string{
			"tenant_id", "event_id", "device_token", "event_type",
			"occurred_time", "state", "reason", "session_id",
		},
	},
	{
		name:   "measurement_rollups",
		source: "measurement_rollups",
		columns: []string{
			"tenant_id", "device_token", "event_type", "name", "bucket",
			"sum_value", "min_value", "max_value", "count_value",
		},
	},
}

// AnalyticsViewNames returns the view names on the read surface, in order. Exported
// for the grant reconciler and for tests that assert the surface's shape.
func AnalyticsViewNames() []string {
	names := make([]string, 0, len(analyticsViews))
	for _, v := range analyticsViews {
		names = append(names, v.name)
	}
	return names
}

// createAnalyticsSchemaStmt creates the read surface's schema. Idempotent.
func createAnalyticsSchemaStmt() string {
	return fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q;", AnalyticsSchema)
}

// readerTenantSearchPath is the value pinned on the function, and the value the
// integrity check requires to still be there.
//
// 🔴 THE GOLDEN CANNOT SEE THIS PIN. normalizeDump strips every line beginning `SET `
// — they are pg_dump's own preamble — so the `SET search_path` clause never reaches
// backend/tools/migrationdiff/golden/event-management.sql. A function re-created
// without it therefore moves no golden and fails no diff. That is why it is checked
// here, from pg_proc.proconfig, rather than left to the schema comparison.
const readerTenantSearchPath = "search_path=pg_catalog"

// readerTenantBody is the function's body, as it is written and as pg_proc.prosrc
// stores it. Shared by the DDL and the integrity check so the two cannot drift: a
// check that compares against its own idea of the body is checking itself.
func readerTenantBody() string {
	// The group-role name is compared as a SQL string LITERAL, so it is single-quoted.
	// %q would render Go's double quotes, which PostgreSQL reads as an IDENTIFIER — the
	// statement then fails with `column "analytics_reader" does not exist`, at migration
	// time, on a fresh install only.
	return fmt.Sprintf(`
		SELECT CASE
			WHEN session_user::text <> '%s'
			 AND session_user::text LIKE '%s=_%%' ESCAPE '='
			THEN substring(session_user::text from %d)
		END
	`,
		strings.ReplaceAll(AnalyticsReaderRole, "'", "''"),
		strings.TrimSuffix(AnalyticsRolePrefix, "_"),
		len(AnalyticsRolePrefix)+1)
}

// createReaderTenantFnStmt defines the function every view's predicate calls.
//
// STABLE rather than IMMUTABLE: it reads the session's identity, which is fixed within
// a statement but not across sessions. `SET search_path = pg_catalog` pins resolution
// so the body cannot be redirected by a caller's search_path.
//
// # 🔴 session_user, NOT current_user, AND THAT WAS A MEASURED CROSS-TENANT READ
//
// This used to key on current_user, with a comment claiming a reader could not change
// it because `SET ROLE` needs a membership it does not have. The design's own
// construction grants exactly that membership: every per-tenant login is created
// `IN ROLE analytics_reader`, and role membership carries the SET option by default.
// So a reader could become the group role — whose name matches the prefix — and read
// the tenant `reader`, which is a legal tenant token. Measured on a live server:
//
//	analytics_acme=> SET ROLE analytics_reader;
//	analytics_acme=> SELECT tenant_id, count(*) FROM analytics.measurement_events GROUP BY 1;
//	 reader | 1
//
// session_user is the AUTHENTICATED identity. `SET ROLE` cannot change it, and the one
// thing that can — `SET SESSION AUTHORIZATION` — is refused to a non-superuser. That
// closes the class rather than the instance.
//
// The explicit exclusion of the group role is kept anyway, as the second layer: it
// means the surface still fails closed if that role is ever given LOGIN, which is one
// checkbox away in any cluster's role management.
//
// The LIKE pattern escapes the underscore with ESCAPE '=' rather than a backslash,
// because a backslash inside a Go string inside a SQL literal is two escapes deep and
// gets miscounted; '=' cannot appear in AnalyticsRolePrefix, so it is unambiguous.
func createReaderTenantFnStmt() string {
	return fmt.Sprintf(`CREATE OR REPLACE FUNCTION %q.reader_tenant() RETURNS text
	LANGUAGE sql STABLE
	SET %s
	AS $reader_tenant$%s$reader_tenant$;`,
		AnalyticsSchema,
		strings.Replace(readerTenantSearchPath, "=", " = ", 1),
		readerTenantBody())
}

// createViewStmt renders one tenant-filtered view.
//
// CREATE OR REPLACE makes it re-runnable, and PostgreSQL permits it here because a
// replay produces the identical column list — replacing a view may only APPEND
// columns, never rename or drop them, which is a second reason the projection is a
// frozen literal rather than a star.
func (v analyticsView) createViewStmt() string {
	cols := make([]string, 0, len(v.columns))
	for _, c := range v.columns {
		cols = append(cols, fmt.Sprintf("%q", c))
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE VIEW %q.%q WITH (security_barrier = true) AS\n"+
			"\tSELECT %s\n\tFROM %q.%q\n\tWHERE %q = %q.reader_tenant();",
		AnalyticsSchema, v.name,
		strings.Join(cols, ", "),
		AnalyticsAreaSchema, v.source,
		"tenant_id", AnalyticsSchema)
}

// dropViewStmt is the rollback counterpart.
func (v analyticsView) dropViewStmt() string {
	return fmt.Sprintf("DROP VIEW IF EXISTS %q.%q;", AnalyticsSchema, v.name)
}

// grantStatements returns the privileges an analytics reader must hold, for the
// named role. USAGE on the surface schema and SELECT on its views; nothing else, and
// specifically nothing on the area schema.
func grantStatements(role string) []string {
	stmts := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %q TO %q;", AnalyticsSchema, role),
	}
	for _, v := range analyticsViews {
		stmts = append(stmts, fmt.Sprintf("GRANT SELECT ON %q.%q TO %q;",
			AnalyticsSchema, v.name, role))
	}
	return stmts
}

// revokeAreaStatements returns the privileges an analytics reader must NOT hold.
//
// 🔴 This is the surface's only backstop, and it exists because RLS could not be
// built (see the file header). A reader that is granted SELECT on a hypertable —
// by a future migration, a debugging session, or a well-meant `GRANT ... ON ALL
// TABLES` — reads every tenant, because the tenant predicate lives in the views it
// would then be bypassing. So the privilege is not merely never granted, it is
// actively removed on every boot.
//
// 🔴 PUBLIC IS ONE OF THE GRANTEES, and leaving it out made the sentence above false in
// the one case that survives everything. Measured: `GRANT USAGE ON SCHEMA
// "event-management" TO PUBLIC` plus `GRANT SELECT ON … TO PUBLIC` gives every reader
// the raw hypertables — and because the reconciler only ever named the roles it found
// in pg_roles, that grant survived every boot forever. PUBLIC is not a role and does
// not appear in pg_roles; it has to be revoked by name. Doing so is safe here because
// the application role OWNS these relations, and an owner's access does not come from
// a grant.
//
// It takes the grantee as a rendered SQL token rather than a bare identifier because
// PUBLIC is a keyword rather than something that can be quoted — see analyticsGrantees.
func revokeAreaStatements(role string) []string {
	return []string{
		fmt.Sprintf("REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA %q FROM %s;",
			AnalyticsAreaSchema, role),
		fmt.Sprintf("REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA %q FROM %s;",
			AnalyticsAreaSchema, role),
		fmt.Sprintf("REVOKE ALL PRIVILEGES ON SCHEMA %q FROM %s;",
			AnalyticsAreaSchema, role),
	}
}

// analyticsRolesQuery finds every role that participates in the read surface: the
// group role and every login role carrying the naming convention. Both are converged,
// because a grant made directly to a per-tenant role would not be caught by
// converging the group alone.
//
// The prefix is passed as a bound parameter with an explicit ESCAPE so an underscore
// in AnalyticsRolePrefix matches literally rather than as LIKE's single-character
// wildcard.
const analyticsRolesQuery = `SELECT rolname FROM pg_roles ` +
	`WHERE rolname = ? OR rolname LIKE ? ESCAPE '=' ORDER BY rolname`

// analyticsRolePattern renders the LIKE pattern matching every per-tenant reader.
func analyticsRolePattern() string {
	return strings.ReplaceAll(AnalyticsRolePrefix, "_", "=_") + "%"
}

// ReconcileAnalyticsSurface converges the read surface — the views AND the privileges
// — on every boot.
//
// It is a reconciler rather than a migration step for one reason that decides the
// design: the group role is created by the DATABASE CLUSTER, asynchronously, and a
// service pod can reach the database before the cluster's role reconciler has run.
// A migration granting to a role that does not exist yet would fail once, be recorded
// as done, and leave a surface nobody could read — with no way back but a schema
// change. Running every boot makes that ordering self-healing, and it also means an
// operator who adds the group role later gets a working surface without one.
//
// 🔴 IT REBUILDS THE VIEWS TOO, AND THAT IS NOT BELT-AND-BRACES. Without it the one
// thing enforcing tenant isolation — the predicate compiled into each view — was
// created ONCE by a migration and never asserted again, while the weaker layer (the
// grants) was converged every boot. That is backwards, and it is not hypothetical:
// this repo's own mutation harness left a database carrying an unfiltered
// measurement_events view, and nothing brought it back, because gormigrate had
// recorded the migration as done. A restart fixed nothing; only a new database did.
// In production the same shape is any damage to a view — a hand-edited definition
// during an investigation, a half-applied replay — leaking every tenant permanently
// while the schema still looks like a schema. The DDL is idempotent CREATE OR
// REPLACE, so re-running it costs a few milliseconds and removes that class.
//
// It is best-effort in the direction that is safe to be best-effort in. A missing
// group role means the surface has no readers, which is the fail-closed outcome, so
// it is reported and startup continues. A failed rebuild or REVOKE is different —
// each leaves a way to read another tenant's rows — so both refuse the boot.
func ReconcileAnalyticsSurface(ctx context.Context, mgr *rdb.RdbManager) error {
	return mgr.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey(analyticsLockName), func() error {
		db := mgr.DB(core.WithSystemContext(ctx))

		// 🔴 THE FUNCTION IS REBUILT UNCONDITIONALLY, THE VIEWS ARE NOT, AND THE
		// ASYMMETRY IS THE POINT RATHER THAN AN INCONSISTENCY.
		//
		// The readiness argument that justifies checking-before-writing the views does
		// not apply here: CREATE OR REPLACE FUNCTION takes no lock on any relation, so
		// re-issuing it cannot contend with a reader's in-flight scan. And the function
		// is the single thing that decides which tenant a session is — a view's
		// predicate only calls it. Leaving THAT to a check is the same mistake as
		// leaving the views to a migration, one level down.
		//
		// Measured, before this ran every boot: replacing the body with
		// `SELECT 'bravo'` left every view's definition and options untouched, so the
		// integrity check passed, the golden did not move — and every reader read
		// bravo's rows.
		if err := execAnalyticsFunction(db); err != nil {
			return err
		}

		// The views next, before any role is granted anything: a boot that is going
		// to fail must fail with the tenant predicate in place, never after handing
		// out SELECT on a view whose filter could not be rebuilt.
		//
		// Checked before it is written, so a healthy boot takes no lock on a relation
		// a reader may be scanning — see analyticsSurfaceIntact.
		intact, reason, err := analyticsSurfaceIntact(db)
		if err != nil {
			return err
		}
		if !intact {
			log.Warn().Str("reason", reason).Str("schema", AnalyticsSchema).
				Msg("The SQL/BI read surface is not what it should be; rebuilding it.")
			if err := execAnalyticsSurface(db); err != nil {
				return err
			}
			// 🔴 And it must be intact AFTERWARDS. Without this, a check that no
			// longer recognises a correct surface — a future PostgreSQL rendering its
			// view definitions differently, say — would rebuild on every boot and take
			// the exclusive lock this design exists to avoid, silently and forever.
			// Re-asking turns that into one loud failure instead.
			if intact, reason, err = analyticsSurfaceIntact(db); err != nil {
				return err
			} else if !intact {
				return fmt.Errorf("the SQL/BI read surface is still wrong after being rebuilt (%s); "+
					"either the rebuild does not produce what the check expects, or something is "+
					"changing it concurrently", reason)
			}
		}

		var roles []string
		if err := db.Raw(analyticsRolesQuery, AnalyticsReaderRole, analyticsRolePattern()).
			Scan(&roles).Error; err != nil {
			return fmt.Errorf("listing the analytics reader roles: %w", err)
		}

		// 🔴 PUBLIC IS ALWAYS CONVERGED, EVEN WITH NO READERS, and that is not
		// symmetry for its own sake. A grant to PUBLIC on the area schema is readable
		// by every login on the instance, present or future, so its removal cannot be
		// conditional on this surface happening to have a reader today.
		for _, stmt := range revokeAreaStatements(publicGrantee) {
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("revoking PUBLIC's privileges on the %q schema: %w",
					AnalyticsAreaSchema, err)
			}
		}

		if len(roles) == 0 {
			// Not an error, and said at INFO with the role name in it because the
			// operator's next question is always "what am I missing".
			log.Info().Str("groupRole", AnalyticsReaderRole).Str("schema", AnalyticsSchema).
				Msg("No analytics reader role exists, so the SQL/BI read surface has no readers. " +
					"Create the group role on the database cluster to enable it.")
			return nil
		}

		granted := 0
		for _, role := range roles {
			// Revoke FIRST. If the boot is going to fail, it must fail with the
			// dangerous privilege already gone rather than still in place.
			for _, stmt := range revokeAreaStatements(quoteIdent(role)) {
				if err := db.Exec(stmt).Error; err != nil {
					return fmt.Errorf("revoking %q's privileges on the %q schema: %w",
						role, AnalyticsAreaSchema, err)
				}
			}
			if role == AnalyticsReaderRole {
				for _, stmt := range grantStatements(role) {
					if err := db.Exec(stmt).Error; err != nil {
						return fmt.Errorf("granting the analytics read surface to %q: %w", role, err)
					}
				}
				granted++
			}
		}

		log.Info().Int("roles", len(roles)).Int("granted", granted).
			Int("views", len(analyticsViews)).Str("schema", AnalyticsSchema).
			Msg("Reconciled the read-only SQL/BI surface's privileges.")
		return nil
	})
}

// analyticsSurfaceStatements is the full ordered DDL for the read surface, shared by
// the migration and by the tests that assert its shape. Kept as one function so a
// test cannot drift from what the migration runs.
func analyticsSurfaceStatements() []string {
	stmts := []string{createAnalyticsSchemaStmt(), createReaderTenantFnStmt()}
	for _, v := range analyticsViews {
		stmts = append(stmts, v.createViewStmt())
	}
	return stmts
}

// surfaceIntegrityQuery reads back what one view actually IS: its storage options, its
// definition as PostgreSQL reconstructs it, and its column list in order.
const surfaceIntegrityQuery = `
	SELECT coalesce(array_to_string(c.reloptions, ','), '') AS opts,
	       pg_get_viewdef(c.oid) AS def,
	       coalesce((SELECT string_agg(a.attname, ',' ORDER BY a.attnum)
	                   FROM pg_attribute a
	                  WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped), '') AS cols
	  FROM pg_class c
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	 WHERE n.nspname = ? AND c.relname = ? AND c.relkind = 'v'`

// analyticsSurfaceIntact reports whether every view is present, carries its frozen
// projection, still filters on the reader's tenant, and is still a security barrier. The
// second return value names the first thing found wrong, for the log line.
//
// 🔴 IT EXISTS TO KEEP THE STEADY STATE LOCK-FREE, WHICH IS AN AVAILABILITY PROPERTY
// RATHER THAN AN OPTIMISATION. `CREATE OR REPLACE VIEW` takes ACCESS EXCLUSIVE on the
// view even when the definition is unchanged, and an analytics reader's in-flight SELECT
// holds ACCESS SHARE — so an unconditional rebuild at startup would make a long BI query
// delay event-management's readiness, and queue every other reader behind it. That is the
// coupling the connection cap exists to prevent, arriving through a different door.
//
// Reading instead of writing takes no lock at all, so a healthy boot never contends with a
// reader. When something IS wrong the rebuild waits for the lock, which is the right
// trade: the tenant boundary is broken, and finishing the repair matters more than
// finishing the boot quickly.
//
// It checks the properties that carry the boundary rather than comparing the whole
// definition byte for byte: pg_get_viewdef reconstructs its own formatting for the
// SELECT list, so a full comparison against generated SQL would differ on every boot and
// rebuild unconditionally — the exact behaviour this avoids. The PREDICATE, though, is
// compared exactly, for the reason below.
func analyticsSurfaceIntact(db *gorm.DB) (bool, string, error) {
	for _, v := range analyticsViews {
		var row struct {
			Opts string
			Def  string
			Cols string
		}
		if err := db.Raw(surfaceIntegrityQuery, AnalyticsSchema, v.name).Scan(&row).Error; err != nil {
			return false, "", fmt.Errorf("reading the %s analytics view: %w", v.name, err)
		}
		switch {
		case row.Cols == "":
			return false, fmt.Sprintf("the %s view is missing", v.name), nil
		case row.Cols != strings.Join(v.columns, ","):
			return false, fmt.Sprintf("the %s view exposes %q, not its declared columns",
				v.name, row.Cols), nil
		case !strings.HasSuffix(collapseSpace(row.Def), expectedViewPredicate()):
			// The one that matters: a view that still exists, still has the right
			// columns, still returns rows — and returns every tenant's.
			//
			// 🔴 A SUFFIX, NOT A SUBSTRING, AND THAT DISTINCTION WAS A MEASURED LEAK.
			// This used to ask whether the definition CONTAINED "reader_tenant()",
			// which `WHERE tenant_id = analytics.reader_tenant() OR true` satisfies
			// while showing every tenant. Anything appended to the predicate moves the
			// end of the definition, so the end is what is compared.
			return false, fmt.Sprintf("the %s view no longer filters on the reader's tenant", v.name), nil
		case !strings.Contains(row.Opts, "security_barrier=true"):
			return false, fmt.Sprintf("the %s view is not a security barrier", v.name), nil
		}
	}
	return functionIntact(db)
}

// expectedViewPredicate is the WHERE clause as PostgreSQL reconstructs it, whitespace
// collapsed.
//
// 🔴 It is the SERVER's rendering, not ours: pg_get_viewdef casts the varchar column and
// parenthesises for itself, so `WHERE "tenant_id" = analytics.reader_tenant()` goes in
// and `WHERE ((tenant_id)::text = analytics.reader_tenant());` comes back. Measured
// identical for all seven views on both supported majors — PostgreSQL 16 (the image the
// goldens are captured against) and PostgreSQL 17 (the deployed operand) — which is what
// makes an exact comparison safe here rather than a source of per-boot rebuilds. If a
// future major renders it differently the reconciler says so loudly rather than
// rebuilding forever; see ReconcileAnalyticsSurface.
func expectedViewPredicate() string {
	return fmt.Sprintf("WHERE ((tenant_id)::text = %s.reader_tenant());", AnalyticsSchema)
}

// collapseSpace reduces every run of whitespace to one space and trims the ends, so a
// comparison is against the SQL rather than against pg_get_viewdef's line breaks.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// functionIntactQuery reads back what reader_tenant() actually IS — not merely that
// something of that name exists.
const functionIntactQuery = `
	SELECT p.prosrc AS src,
	       coalesce(array_to_string(p.proconfig, ','), '') AS cfg,
	       p.prosecdef AS secdef,
	       p.provolatile::text AS vol
	  FROM pg_proc p
	  JOIN pg_namespace n ON n.oid = p.pronamespace
	 WHERE n.nspname = ? AND p.proname = 'reader_tenant' AND p.pronargs = 0`

// functionIntact reports whether the identity function is still the one this code wrote.
//
// 🔴 EVERY PROPERTY HERE HAS BEEN MEASURED TO LEAK WHEN IT IS ABSENT, and none of them
// is visible to any other gate in the repo:
//
//	prosrc      replaced with `SELECT 'bravo'` — every view's definition and options
//	            unchanged, so a view-only check passes and every reader reads bravo.
//	proconfig   the search_path pin is dropped by any CREATE OR REPLACE that omits it,
//	            and normalizeDump strips `SET ` lines, so the golden never carried it.
//	prosecdef   a SECURITY DEFINER re-creation runs the body as the OWNER, which is the
//	            one identity for which the whole derivation is meaningless.
//	provolatile IMMUTABLE lets the planner fold one session's answer into a cached plan.
//
// The function is also rebuilt unconditionally on every boot, so in practice this check
// reports rather than repairs. Both are kept: the rebuild fixes it, and this says so.
func functionIntact(db *gorm.DB) (bool, string, error) {
	var row struct {
		Src    string
		Cfg    string
		Secdef bool
		Vol    string
	}
	if err := db.Raw(functionIntactQuery, AnalyticsSchema).Scan(&row).Error; err != nil {
		return false, "", fmt.Errorf("reading the reader_tenant function: %w", err)
	}
	switch {
	case row.Src == "":
		return false, "the reader_tenant() function is missing", nil
	case row.Src != readerTenantBody():
		return false, "the reader_tenant() function does not have the body this build wrote", nil
	case !strings.Contains(row.Cfg, readerTenantSearchPath):
		return false, "the reader_tenant() function has lost its pinned search_path", nil
	case row.Secdef:
		return false, "the reader_tenant() function is SECURITY DEFINER", nil
	case row.Vol != "s":
		return false, fmt.Sprintf("the reader_tenant() function is %q, not STABLE", row.Vol), nil
	}
	return true, "", nil
}

// publicGrantee is PUBLIC rendered as a grantee. It is a keyword rather than an
// identifier, so it cannot be quoted — `REVOKE … FROM "public"` names a ROLE called
// public, which is not the same thing and normally does not exist.
const publicGrantee = "PUBLIC"

// quoteIdent renders a role name as a quoted SQL identifier, doubling any embedded
// quote. Role names here come from pg_roles rather than from user input, so this is
// belt-and-braces — but it is the difference between a helper that is safe wherever it
// is reused and one that is safe only where it happens to be called today.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// execAnalyticsFunction creates the schema and the reader_tenant() function — the part
// of the surface that takes no relation lock and is therefore re-issued on every boot.
func execAnalyticsFunction(tx *gorm.DB) error {
	for _, stmt := range []string{createAnalyticsSchemaStmt(), createReaderTenantFnStmt()} {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("building the analytics reader identity: %w", err)
		}
	}
	return nil
}

// execAnalyticsSurface runs the surface's DDL against tx.
func execAnalyticsSurface(tx *gorm.DB) error {
	for _, stmt := range analyticsSurfaceStatements() {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("building the analytics read surface: %w", err)
		}
	}
	return nil
}
