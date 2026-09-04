// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The unit tests here assert the SHAPE of the read surface's SQL. They cannot assert
// its BEHAVIOUR — whether a reader can actually reach another tenant's rows is a
// question about grants, view expansion and current_user, none of which sqlite has.
// That is what analytics_integration_test.go is for, and neither file substitutes for
// the other: this one fails fast on a predicate somebody deleted while editing, that
// one fails on a boundary that only a real connection can test.

// TestEveryAnalyticsViewCarriesTheTenantPredicate is the cheapest possible guard on the
// one clause the whole surface rests on.
//
// 🔴 It is worth having precisely because the clause is easy to lose and impossible to
// miss the absence of from the outside: a view without it still exists, still has the
// right columns, still returns rows, and returns EVERY tenant's.
func TestEveryAnalyticsViewCarriesTheTenantPredicate(t *testing.T) {
	if len(analyticsViews) == 0 {
		t.Fatal("no analytics views are declared, so this test would pass having checked nothing")
	}
	for _, v := range analyticsViews {
		stmt := v.createViewStmt()
		want := fmt.Sprintf(`WHERE "tenant_id" = %q.reader_tenant()`, AnalyticsSchema)
		if !strings.Contains(stmt, want) {
			t.Errorf("view %s does not filter on the reader's tenant; want %q in:\n%s", v.name, want, stmt)
		}
		if !strings.Contains(stmt, "security_barrier = true") {
			t.Errorf("view %s is not a security barrier, so a reader's own WHERE clause can be "+
				"evaluated below the tenant predicate:\n%s", v.name, stmt)
		}
		if strings.Contains(stmt, "SELECT *") {
			t.Errorf("view %s projects a star; the column list must be frozen so a fresh install "+
				"and an upgraded one build the same view:\n%s", v.name, stmt)
		}
		if len(v.columns) == 0 {
			t.Errorf("view %s declares no columns", v.name)
		}
	}
}

// TestAnalyticsGrantsNeverTouchTheAreaSchema pins the grant boundary in both directions.
//
// With row-level security unavailable (TimescaleDB refuses to combine it with
// compression — see analytics.go), the fact that a reader holds NOTHING on the area
// schema is not a nicety; it is the boundary. A GRANT written here by accident would be
// the whole leak.
func TestAnalyticsGrantsNeverTouchTheAreaSchema(t *testing.T) {
	grants := grantStatements(AnalyticsReaderRole)
	if len(grants) != len(analyticsViews)+1 {
		t.Fatalf("expected one USAGE grant plus one per view (%d), got %d",
			len(analyticsViews)+1, len(grants))
	}
	for _, stmt := range grants {
		if strings.Contains(stmt, AnalyticsAreaSchema) {
			t.Errorf("a grant names the area schema, which the read role must never reach: %s", stmt)
		}
		if !strings.Contains(stmt, "SELECT") && !strings.Contains(stmt, "USAGE") {
			t.Errorf("a grant confers something other than read access: %s", stmt)
		}
	}

	revokes := revokeAreaStatements(AnalyticsReaderRole)
	if len(revokes) == 0 {
		t.Fatal("nothing is revoked, so an over-broad grant on the area schema would survive a boot")
	}
	for _, stmt := range revokes {
		if !strings.Contains(stmt, AnalyticsAreaSchema) {
			t.Errorf("a revoke does not name the area schema: %s", stmt)
		}
	}
}

// TestReaderTenantIsDerivedFromCurrentUser pins the identity mechanism.
//
// A session setting would be forgeable: custom GUCs are USERSET, so a reader could point
// one at another tenant in its own session. current_user cannot be chosen. If this test
// starts failing because the function was rewritten to read a setting, the surface has
// silently become a cross-tenant read.
func TestReaderTenantIsDerivedFromCurrentUser(t *testing.T) {
	fn := createReaderTenantFnStmt()
	if !strings.Contains(fn, "current_user") {
		t.Error("reader_tenant() no longer derives the tenant from current_user")
	}
	if strings.Contains(fn, "current_setting") {
		t.Error("reader_tenant() reads a session setting, which a reader can set to any tenant it likes")
	}
	// The offset must skip exactly the prefix, or a reader named analytics_acme reads a
	// tenant called "_acme" (or "cme") and matches nothing — which fails safe but silently.
	if !strings.Contains(fn, fmt.Sprintf("from %d", len(AnalyticsRolePrefix)+1)) {
		t.Errorf("the substring offset does not skip %q exactly:\n%s", AnalyticsRolePrefix, fn)
	}
}

// TestAnalyticsRolePatternEscapesTheUnderscore guards a one-character defect with a
// silent blast radius: an unescaped `_` is LIKE's single-character wildcard, so
// `analytics_%` would also match a role named `analyticsX...`. The reconciler would then
// revoke privileges from roles that have nothing to do with this surface.
func TestAnalyticsRolePatternEscapesTheUnderscore(t *testing.T) {
	got := analyticsRolePattern()
	// Remove the escaped pairs first; anything left is a live wildcard.
	if bare := strings.ReplaceAll(got, "=_", ""); strings.Contains(bare, "_") {
		t.Errorf("the LIKE pattern carries an unescaped underscore wildcard: %q", got)
	}
	if want := "analytics=_%"; got != want {
		t.Errorf("analyticsRolePattern() = %q, want %q", got, want)
	}
}

// TestAnalyticsViewsOnlyNameColumnsTheModelsStillHave is the drift guard.
//
// The frozen projections are deliberately allowed to LAG the live models: a column added
// later is simply not on the read surface until somebody puts it there, which is the
// snapshot rule working as intended. What they must never do is name a column that no
// longer exists — a rename or a drop would make this migration fail on a FRESH install
// while every existing database, whose views were built before the change, stays healthy.
// That is the exact asymmetry the snapshot rule exists to prevent, and the only place it
// can bite a view.
//
// The rollup view is checked against the aggregate's own frozen output columns rather
// than a model, because a continuous aggregate has no Go type.
func TestAnalyticsViewsOnlyNameColumnsTheModelsStillHave(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	models := map[string]interface{}{
		"events":              &Event{},
		"location_events":     &LocationEvent{},
		"measurement_events":  &MeasurementEvent{},
		"alert_events":        &AlertEvent{},
		"event_anchors":       &EventAnchor{},
		"state_change_events": &StateChangeEvent{},
	}
	// The aggregate's columns, frozen alongside the aggregate's own definition in the
	// baseline. Restated rather than imported for the same reason the baseline restates
	// its bucket width: a shared symbol would let one side redefine the other's history.
	rollupColumns := map[string]bool{
		"tenant_id": true, "device_token": true, "event_type": true, "name": true,
		"bucket": true, "sum_value": true, "min_value": true, "max_value": true,
		"count_value": true,
	}

	checked := 0
	for _, v := range analyticsViews {
		if v.source == "measurement_rollups" {
			for _, c := range v.columns {
				if !rollupColumns[c] {
					t.Errorf("the rollup view names %q, which the continuous aggregate does not produce", c)
				}
			}
			checked++
			continue
		}
		m, ok := models[v.source]
		if !ok {
			t.Fatalf("view %s reads %q, which this test has no model for — add it rather than "+
				"leaving the projection unchecked", v.name, v.source)
		}
		if err := db.AutoMigrate(m); err != nil {
			t.Fatalf("automigrate %s: %v", v.source, err)
		}
		cols, err := db.Migrator().ColumnTypes(m)
		if err != nil {
			t.Fatalf("read columns of %s: %v", v.source, err)
		}
		have := map[string]bool{}
		for _, c := range cols {
			have[c.Name()] = true
		}
		for _, c := range v.columns {
			if !have[c] {
				t.Errorf("view %s names column %q, which %s no longer has — a fresh install would "+
					"fail this migration while every existing database looks healthy",
					v.name, c, v.source)
			}
		}
		checked++
	}
	// 🔴 The negative control. "No columns went missing" is also what a loop over an
	// empty slice reports, and the whole file would then be green having compared nothing.
	if checked != len(analyticsViews) || checked == 0 {
		t.Fatalf("checked %d of %d views", checked, len(analyticsViews))
	}
}

// TestAnalyticsMigrationIsRegisteredLast pins the ordering the view bodies depend on.
//
// The location view names accuracy, speed and heading, which an earlier migration
// appends. Registered before it, this migration would fail on a fresh install with an
// error about a column — pointing at the table rather than at the ordering.
func TestAnalyticsMigrationIsRegisteredLast(t *testing.T) {
	if len(Migrations) == 0 {
		t.Fatal("the migration chain is empty")
	}
	last := Migrations[len(Migrations)-1]
	if want := NewAnalyticsSurfaceSchema().ID; last.ID != want {
		t.Errorf("the last migration is %s, want the analytics surface (%s)", last.ID, want)
	}
	seen := map[string]bool{}
	for _, m := range Migrations {
		if seen[m.ID] {
			t.Errorf("duplicate migration id %s", m.ID)
		}
		seen[m.ID] = true
	}
}
