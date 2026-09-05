// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"testing"
)

// The SQL/BI surface shipped with one grant covering every view, which handed any
// declared BI reader lat/lon/elevation/accuracy/speed/heading. The platform treats
// position as a SEPARATE authority everywhere else — location:read is held out of the
// read-only viewer baseline and gates the locationEvents query on its own — so the SQL
// surface was quietly wider than the API surface for the one class of data the split
// exists for.
//
// It cannot be closed by "checking the authority": a BI reader authenticates as a
// PostgreSQL role and carries no claims, and it is declared in the deployment rather
// than in the tenant's role model. The only thing a database can express is a grant, so
// position became its own grant on its own group role.
//
// 🔴 THESE ARE SHAPE TESTS, AND THE GRANT BOUNDARY IS INVISIBLE TO EVERY OTHER GATE.
// The migration differ dumps with --no-privileges, so no ACL can move a golden; a
// reader's actual reach is asserted in analytics_integration_test.go against a real
// connection. What this file pins is the statement set the reconciler will issue, which
// is the thing a refactor breaks.

// The position columns are the ones location:read exists to gate, so the view carrying
// them must be the one on the position group — named by column rather than by view name,
// so moving a coordinate onto another view fails here.
var positionColumns = []string{"latitude", "longitude", "elevation", "accuracy", "speed", "heading"}

// Every view that carries a position column is granted to the position group, and every
// view that does not is granted to the general reader group. This is the boundary
// itself, stated as a property of the columns rather than as a list of names.
func TestPositionIsGrantedOnlyToTheLocationGroup(t *testing.T) {
	if len(analyticsViews) == 0 {
		t.Fatal("no analytics views are declared, so this test would pass having checked nothing")
	}
	sawPosition := false
	for _, v := range analyticsViews {
		carries := false
		for _, c := range v.columns {
			for _, p := range positionColumns {
				if c == p {
					carries = true
				}
			}
		}
		switch {
		case carries:
			sawPosition = true
			if v.group != AnalyticsLocationReaderRole {
				t.Errorf("the %s view carries position columns but is granted to %q; a reader "+
					"holding only the general grant would read coordinates", v.name, v.group)
			}
		case v.group != AnalyticsReaderRole:
			t.Errorf("the %s view carries no position columns but is granted to %q; the position "+
				"group must hold nothing else, or membership in it stops meaning anything",
				v.name, v.group)
		}
	}
	// The control. Without it, deleting every position column from the surface would
	// make the loop above vacuous and this test green.
	if !sawPosition {
		t.Fatal("no view on the surface carries a position column, so the split under test " +
			"is not exercised by anything here")
	}
}

// The general reader group's grants name location_events nowhere. Stated separately from
// the property above because it is the assertion an operator's question maps onto —
// "what does an ordinary BI reader get?" — and because a grant loop that ignored the
// group filter entirely would still satisfy a per-view check.
func TestGeneralReaderIsNotGrantedTheLocationView(t *testing.T) {
	for _, stmt := range grantStatements(AnalyticsReaderRole) {
		if strings.Contains(stmt, `"location_events"`) {
			t.Errorf("the general analytics reader is granted the location view: %s", stmt)
		}
	}

	// The counterweight: the position group DOES get it, so the fix is a move and not a
	// deletion. A surface where nobody can read position is not the same product.
	granted := false
	for _, stmt := range grantStatements(AnalyticsLocationReaderRole) {
		if strings.Contains(stmt, `"location_events"`) && strings.Contains(stmt, "SELECT") {
			granted = true
		}
	}
	if !granted {
		t.Errorf("no group role is granted the location view, so position is unreadable to every "+
			"BI tool rather than gated: %v", grantStatements(AnalyticsLocationReaderRole))
	}
}

// The general reader still reaches the base envelope, which is what keeps this surface
// aligned with the GraphQL one: `events` returns a location event's envelope under plain
// event:read, because a base event carries no coordinates. A fix that hid the fact a
// location event happened would be stricter than the boundary it is matching.
func TestGeneralReaderStillReadsTheBaseEnvelope(t *testing.T) {
	found := false
	for _, stmt := range grantStatements(AnalyticsReaderRole) {
		if strings.Contains(stmt, fmt.Sprintf("%q.%q", AnalyticsSchema, "events")) {
			found = true
		}
	}
	if !found {
		t.Error("the general analytics reader cannot read analytics.events, so it cannot see " +
			"that a location event occurred at all — stricter than the authority split it mirrors")
	}
	for _, v := range analyticsViews {
		if v.name == "events" {
			for _, c := range v.columns {
				for _, p := range positionColumns {
					if c == p {
						t.Errorf("the base envelope view now projects %q, so the general grant "+
							"carries position after all", c)
					}
				}
			}
		}
	}
}

// Every group role's privileges are converged in BOTH directions on every boot, and the
// REVOKES COME FIRST. The revoke is what makes the split reach an install that already
// ran the earlier surface: such a database holds SELECT on analytics.location_events for
// analytics_reader, and a reconciler that only ever grants would leave it there forever
// while the repository looked fixed.
//
// 🔴 THIS TEST USED TO BE NAMED FOR AN ORDERING IT DID NOT CHECK. It asserted only that
// revoke statements existed and named the right schema, so a mutant that issued the
// grants BEFORE the revokes — which converges to a surface with nothing granted at all —
// passed it, and was caught only by the integration suite. That suite runs on a laptop
// and in no CI job, which made the ordering property effectively untested. It is checked
// here now by reading the statement sequence, which is why convergeStatements returns one
// rather than issuing it.
func TestGroupPrivilegesAreRevokedBeforeTheyAreGranted(t *testing.T) {
	for _, group := range analyticsGroupRoles() {
		stmts := convergeStatements(granteeFor(group))

		lastRevoke, firstGrant := -1, -1
		for i, stmt := range stmts {
			switch {
			case strings.HasPrefix(stmt, "REVOKE"):
				lastRevoke = i
			case strings.HasPrefix(stmt, "GRANT"):
				if firstGrant < 0 {
					firstGrant = i
				}
			default:
				t.Errorf("%q: a convergence statement neither grants nor revokes: %s", group, stmt)
			}
		}

		if lastRevoke < 0 {
			t.Fatalf("nothing is revoked from %q, so a grant made by an earlier version of "+
				"this code survives every boot", group)
		}
		if firstGrant < 0 {
			t.Fatalf("nothing is granted to %q, so the surface has no readers at all", group)
		}
		if lastRevoke > firstGrant {
			t.Errorf("%q is granted at statement %d before the revoke at %d; a boot that fails "+
				"in between leaves the dangerous privilege in place, and a boot that succeeds "+
				"revokes what it just granted:\n%s",
				group, firstGrant, lastRevoke, strings.Join(stmts, "\n"))
		}

		surface := 0
		for _, stmt := range revokeSurfaceStatements(quoteIdent(group)) {
			if !strings.Contains(stmt, fmt.Sprintf("%q", AnalyticsSchema)) {
				t.Errorf("a surface revoke does not name the surface schema: %s", stmt)
			}
			if !strings.HasPrefix(stmt, "REVOKE") {
				t.Errorf("a surface revoke does not revoke: %s", stmt)
			}
			surface++
		}
		if surface == 0 {
			t.Errorf("nothing is revoked from %q on the surface schema", group)
		}
	}
}

// 🔴 A PER-TENANT READER IS CONVERGED TOO, AND HOLDS NOTHING BACK. It reads through
// membership in a group role, so any privilege it holds BY NAME was granted by a hand
// this reconciler exists to undo — on the AREA schema, where it would reach every
// tenant's raw rows, and on the SURFACE schema, where a direct grant of the position view
// walks straight around the split.
//
// The first version of this change converged a reader on the area schema and then
// `continue`d, so a direct grant on analytics.location_events outlived every boot.
func TestPerTenantReadersAreConvergedAndGrantedNothing(t *testing.T) {
	reader := AnalyticsRolePrefix + "acme"
	g := granteeFor(reader)

	if g.grantRole != "" {
		t.Errorf("a per-tenant reader is granted the surface directly (as %q); it must read "+
			"through its membership, or revoking the group's grant would not reach it", g.grantRole)
	}

	stmts := convergeStatements(g)
	for _, stmt := range stmts {
		if strings.HasPrefix(stmt, "GRANT") {
			t.Errorf("a per-tenant reader is granted something directly: %s", stmt)
		}
	}

	// Both schemas, named explicitly. Converging one and not the other is exactly the
	// defect this test exists for, and "some revokes were issued" cannot see it.
	for _, schema := range []string{AnalyticsAreaSchema, AnalyticsSchema} {
		found := false
		for _, stmt := range stmts {
			if strings.Contains(stmt, fmt.Sprintf("%q", schema)) {
				found = true
			}
		}
		if !found {
			t.Errorf("a per-tenant reader's privileges on the %q schema are never revoked:\n%s",
				schema, strings.Join(stmts, "\n"))
		}
	}
}

// 🔴 THE CONVERGENCE PLAN NAMES EVERY ENUMERATED ROLE, GROUP OR NOT, AND PUBLIC FIRST.
// Two mutants — skipping per-tenant readers, and converging PUBLIC on the area schema
// alone — survived every gate that runs in CI, because the only instrument that could
// see them is the integration suite, which needs a real Postgres and is wired into no
// workflow. Asking the PLAN rather than the loop is what brings that property back
// inside a gate that runs.
func TestEveryEnumeratedRoleIsInTheConvergencePlan(t *testing.T) {
	reader := AnalyticsRolePrefix + "acme"
	roles := []string{AnalyticsReaderRole, AnalyticsLocationReaderRole, reader}

	plan := analyticsGrantees(roles)

	if len(plan) != len(roles)+1 {
		t.Fatalf("the plan converges %d identities for %d roles; every role plus PUBLIC must "+
			"be in it: %+v", len(plan), len(roles), plan)
	}
	if plan[0].token != publicGrantee {
		t.Errorf("PUBLIC is not converged first (got %q); a boot that fails partway must have "+
			"taken back the widest grantee already", plan[0].token)
	}
	for _, want := range roles {
		found := false
		for _, g := range plan {
			if g.token == quoteIdent(want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not in the convergence plan, so a privilege granted to it by hand "+
				"survives every boot", want)
		}
	}
	// And the per-tenant reader is in the plan WITHOUT being granted anything, which is
	// the distinction the plan exists to carry.
	for _, g := range plan {
		if g.token == quoteIdent(reader) && g.grantRole != "" {
			t.Errorf("the per-tenant reader %q is granted %q directly; it must read through "+
				"membership", reader, g.grantRole)
		}
	}
}

// PUBLIC is converged even when the surface has no readers at all. A grant to PUBLIC is
// readable by every login on the instance, present or future, so taking it back cannot be
// conditional on a reader happening to exist today.
func TestPublicIsConvergedWithNoReaders(t *testing.T) {
	plan := analyticsGrantees(nil)

	if len(plan) != 1 || plan[0].token != publicGrantee {
		t.Fatalf("with no roles the plan must still converge PUBLIC and nothing else, got %+v", plan)
	}
	if plan[0].grantRole != "" {
		t.Errorf("PUBLIC is granted %q; it must only ever be revoked from", plan[0].grantRole)
	}
}

// PUBLIC is converged on both schemas too, and is rendered as the KEYWORD. It is not a
// role, does not appear in pg_roles, and `FROM "public"` names a role that normally does
// not exist — so a helper that quoted for itself would be silently wrong for the one
// grantee every login on the instance inherits from.
func TestPublicIsConvergedOnBothSchemasAsAKeyword(t *testing.T) {
	stmts := convergeStatements(analyticsGrantee{token: publicGrantee})

	for _, stmt := range stmts {
		if strings.HasPrefix(stmt, "GRANT") {
			t.Errorf("PUBLIC is granted something: %s", stmt)
		}
		if strings.Contains(stmt, `"PUBLIC"`) || strings.Contains(stmt, `"public"`) {
			t.Errorf("PUBLIC is quoted as an identifier, which names a role rather than the "+
				"keyword: %s", stmt)
		}
	}
	for _, schema := range []string{AnalyticsAreaSchema, AnalyticsSchema} {
		found := false
		for _, stmt := range stmts {
			if strings.Contains(stmt, fmt.Sprintf("%q", schema)) && strings.HasSuffix(stmt, "FROM PUBLIC;") {
				found = true
			}
		}
		if !found {
			t.Errorf("PUBLIC's privileges on the %q schema are never revoked:\n%s",
				schema, strings.Join(stmts, "\n"))
		}
	}
}

// A view must name a grantee that is actually converged. An empty group grants to nobody
// and an unknown one grants to nothing — both silent, both invisible to the schema diff,
// which strips privileges from its dump entirely.
func TestEveryViewDeclaresAConvergedGrantee(t *testing.T) {
	if err := analyticsViewsDeclareTheirGrantee(); err != nil {
		t.Fatalf("the declared surface is not grantable: %v", err)
	}

	// The negative control, applied and reverted in place: the check has to be able to
	// FAIL, or its green says nothing.
	original := analyticsViews
	t.Cleanup(func() { analyticsViews = original })

	analyticsViews = []analyticsView{{name: "probe", source: "events", columns: []string{"tenant_id"}}}
	if err := analyticsViewsDeclareTheirGrantee(); err == nil {
		t.Error("a view with no grantee was accepted, so the check cannot catch one")
	}

	analyticsViews = []analyticsView{{name: "probe", source: "events",
		columns: []string{"tenant_id"}, group: "some_other_role"}}
	if err := analyticsViewsDeclareTheirGrantee(); err == nil {
		t.Error("a view granted to a role the reconciler never converges was accepted")
	}
}

// The role query has to find both group roles as well as the per-tenant readers. A group
// role it misses is one whose privileges on the AREA schema are never revoked, which is
// the single backstop standing between a stray grant and every tenant's rows.
func TestRoleQueryNamesEveryGroupRole(t *testing.T) {
	query := analyticsRolesQuery()
	args := analyticsRolesQueryArgs()

	if want := len(analyticsGroupRoles()) + 1; len(args) != want {
		t.Fatalf("the role query binds %d arguments, want %d (one per group role plus the prefix)",
			len(args), want)
	}
	if got := strings.Count(query, "?"); got != len(args) {
		t.Fatalf("the role query has %d placeholders for %d arguments", got, len(args))
	}
	for i, group := range analyticsGroupRoles() {
		if args[i] != group {
			t.Errorf("argument %d is %v, want the group role %q", i, args[i], group)
		}
	}
	if args[len(args)-1] != analyticsRolePattern() {
		t.Errorf("the last argument is %v, want the reader prefix pattern %q",
			args[len(args)-1], analyticsRolePattern())
	}
}

// isAnalyticsGroupRole decides which roles get grants at all, so it has to be exact: a
// per-tenant reader that it called a group role would be granted the surface directly,
// which is a privilege no revoke of the group's membership could take away.
func TestOnlyTheGroupRolesAreTreatedAsGroups(t *testing.T) {
	for _, group := range analyticsGroupRoles() {
		if !isAnalyticsGroupRole(group) {
			t.Errorf("%q is a declared group role and is not recognised as one", group)
		}
	}
	for _, notAGroup := range []string{
		"analytics_acme",                 // an ordinary per-tenant reader
		"analytics_location_reader_acme", // a reader whose tenant STARTS with a group name
		"analytics_reader2",              // a near-miss on the group name
		"postgres",
	} {
		if isAnalyticsGroupRole(notAGroup) {
			t.Errorf("%q is not a group role but is treated as one, so it would be granted the "+
				"surface directly rather than through membership", notAGroup)
		}
	}
}
