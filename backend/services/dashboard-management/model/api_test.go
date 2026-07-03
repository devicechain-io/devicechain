// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func strp(s string) *string { return &s }

// newTestApi spins up an in-memory sqlite database with the tenant-scope
// callbacks registered and the Dashboard table migrated, then wraps it in an Api
// so the CRUD path can be exercised exactly as production does.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	// Register the token-grammar callbacks too, so the CRUD path is exercised
	// exactly as production does (ADR-042 P2): a non-conforming token fixture would
	// fail here as it would live.
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&Dashboard{}, &DashboardVersion{}))
	// Mirror the migration's per-tenant partial unique index (ADR-042 P1) so the
	// token-uniqueness tests run against the real constraint, not a bare table.
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &Dashboard{}))
	return NewApi(&rdb.RdbManager{Database: db})
}

// TestDashboardCrud exercises create -> read -> update -> delete.
func TestDashboardCrud(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "fleet",
		Name:       strp("Fleet overview"),
		Definition: `{"schemaVersion":1,"widgets":[]}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "fleet", created.Token)
	assert.Equal(t, "Fleet overview", created.Name.String)

	// Read back by token.
	found, err := api.DashboardsByToken(ctx, []string{"fleet"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.JSONEq(t, `{"schemaVersion":1,"widgets":[]}`, string(found[0].Definition))

	// Update: rename + new definition.
	updated, err := api.UpdateDashboard(ctx, "fleet", &DashboardCreateRequest{
		Token:      "fleet",
		Name:       strp("Fleet v2"),
		Definition: `{"schemaVersion":1,"widgets":[{"id":"w1"}]}`,
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Fleet v2", updated.Name.String)
	assert.JSONEq(t, `{"schemaVersion":1,"widgets":[{"id":"w1"}]}`, string(updated.Definition))

	// Search finds it.
	page, err := api.Dashboards(ctx, DashboardSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	assert.Len(t, page.Results, 1)

	// Delete reports true, then the row is gone.
	ok, err := api.DeleteDashboard(ctx, "fleet")
	require.NoError(t, err)
	assert.True(t, ok)

	gone, err := api.DashboardsByToken(ctx, []string{"fleet"})
	require.NoError(t, err)
	assert.Empty(t, gone)

	// Deleting a missing dashboard reports false, not an error.
	ok, err = api.DeleteDashboard(ctx, "fleet")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestTokenReusableAfterDelete guards the hard-delete (Unscoped) fix: deleting a
// dashboard must free its token so the same token can be created again. Under a
// soft-delete — the original bug — the deleted row would remain and keep the token
// occupying the unique index, so this re-create would fail with a duplicate-key
// error. A revert to soft-delete therefore turns this test red.
func TestTokenReusableAfterDelete(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	def := `{"schemaVersion":1,"widgets":[]}`

	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "reuse", Definition: def})
	require.NoError(t, err)

	ok, err := api.DeleteDashboard(ctx, "reuse")
	require.NoError(t, err)
	require.True(t, ok)

	// The token must be free again — no lingering soft-deleted row on the unique index.
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "reuse", Definition: def})
	require.NoError(t, err, "token should be reusable after delete (hard delete frees the unique index)")
}

// TestInvalidDefinitionRejected confirms a non-JSON definition is refused on both
// create and update rather than stored silently.
func TestInvalidDefinitionRejected(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "bad",
		Definition: "not json",
	})
	assert.ErrorIs(t, err, ErrInvalidDefinition)

	// A valid one, then an invalid update.
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token:      "ok",
		Definition: `{"schemaVersion":1}`,
	})
	require.NoError(t, err)

	_, err = api.UpdateDashboard(ctx, "ok", &DashboardCreateRequest{
		Token:      "ok",
		Definition: "}{",
	}, nil)
	assert.ErrorIs(t, err, ErrInvalidDefinition)
}

// TestNonObjectDefinitionRejected confirms a well-formed-but-non-object document
// (a scalar or array) is refused — it would only fail later at render time.
func TestNonObjectDefinitionRejected(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, def := range []string{`42`, `true`, `"hello"`, `null`, `[]`, `[{"id":"w1"}]`} {
		_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "scalar", Definition: def})
		assert.ErrorIsf(t, err, ErrInvalidDefinition, "definition %q must be rejected", def)
	}
}

// TestOversizedDefinitionRejected confirms a definition beyond the size cap is
// refused before it can exhaust shared storage.
func TestOversizedDefinitionRejected(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// A well-formed object whose padding pushes it past the cap.
	big := `{"schemaVersion":1,"pad":"` + strings.Repeat("x", maxDefinitionBytes) + `"}`
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "big", Definition: big})
	assert.ErrorIs(t, err, ErrDefinitionTooLarge)
}

// TestTenantIsolation confirms a dashboard created under one tenant is invisible
// to another (the storage-layer isolation the whole platform relies on).
func TestTenantIsolation(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")

	_, err := api.CreateDashboard(acme, &DashboardCreateRequest{
		Token:      "shared-token",
		Definition: `{"schemaVersion":1}`,
	})
	require.NoError(t, err)

	found, err := api.DashboardsByToken(other, []string{"shared-token"})
	require.NoError(t, err)
	assert.Empty(t, found, "dashboard must not be visible across tenants")
}

// TestUpdateOptimisticConcurrency confirms the expectedUpdatedAt precondition:
// a stale timestamp is rejected with ErrConflict, the current one succeeds, and a
// nil precondition is unconditional (backward-compatible last-write-wins).
func TestUpdateOptimisticConcurrency(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: `{"schemaVersion":1}`})
	require.NoError(t, err)
	current := created.UpdatedAt.Format(time.RFC3339)

	req := func() *DashboardCreateRequest {
		return &DashboardCreateRequest{Token: "d", Definition: `{"schemaVersion":1,"widgets":[]}`}
	}

	// A wrong expected timestamp is a conflict (stands in for a concurrent writer
	// having moved the row on; deterministic vs. relying on sub-second timing).
	_, err = api.UpdateDashboard(ctx, "d", req(), strp("2000-01-01T00:00:00Z"))
	assert.ErrorIs(t, err, ErrConflict)

	// The current timestamp passes the precondition.
	_, err = api.UpdateDashboard(ctx, "d", req(), &current)
	require.NoError(t, err)

	// No precondition → unconditional save.
	_, err = api.UpdateDashboard(ctx, "d", req(), nil)
	require.NoError(t, err)
}

// TestPublishVersionsAndRollback exercises the versioning lifecycle: publish
// snapshots the draft, versions list newest-first with the right numbers/labels/
// snapshots, and rollback re-drafts a snapshot without deleting history.
func TestPublishVersionsAndRollback(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	defA := `{"schemaVersion":1,"widgets":[{"id":"a"}]}`
	defB := `{"schemaVersion":1,"widgets":[{"id":"b"}]}`
	defC := `{"schemaVersion":1,"widgets":[{"id":"c"}]}`

	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)

	// Publish A as v1.
	v1, err := api.PublishDashboard(ctx, "d", strp("v1.0.0"), nil, "alice")
	require.NoError(t, err)
	assert.Equal(t, int32(1), v1.Version)
	assert.Equal(t, "alice", v1.PublishedBy)

	// Move the draft to B, publish as v2.
	_, err = api.UpdateDashboard(ctx, "d", &DashboardCreateRequest{Token: "d", Definition: defB}, nil)
	require.NoError(t, err)
	v2, err := api.PublishDashboard(ctx, "d", strp("v2.0.0"), strp("second"), "bob")
	require.NoError(t, err)
	assert.Equal(t, int32(2), v2.Version)

	// Versions list is newest-first with per-version snapshots preserved.
	versions, err := api.DashboardVersions(ctx, "d")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, int32(2), versions[0].Version)
	assert.Equal(t, int32(1), versions[1].Version)
	assert.JSONEq(t, defB, string(versions[0].Definition))
	assert.JSONEq(t, defA, string(versions[1].Definition))

	// Move the draft to C (unpublished), then roll back to v1 (defA).
	_, err = api.UpdateDashboard(ctx, "d", &DashboardCreateRequest{Token: "d", Definition: defC}, nil)
	require.NoError(t, err)
	rolled, err := api.RollbackDashboard(ctx, "d", 1)
	require.NoError(t, err)
	assert.JSONEq(t, defA, string(rolled.Definition))

	// History is append-only — rollback didn't delete any version.
	versions, err = api.DashboardVersions(ctx, "d")
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	// Rolling back to a non-existent version errors, not silently succeeds.
	_, err = api.RollbackDashboard(ctx, "d", 99)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestVersionsTenantIsolation confirms versioning is confined to the owning tenant:
// another tenant can neither see nor publish against the dashboard.
func TestVersionsTenantIsolation(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")

	_, err := api.CreateDashboard(acme, &DashboardCreateRequest{Token: "d", Definition: `{"schemaVersion":1}`})
	require.NoError(t, err)
	_, err = api.PublishDashboard(acme, "d", nil, nil, "alice")
	require.NoError(t, err)

	// The other tenant can't see the dashboard, so versions/publish resolve to
	// "no such dashboard" rather than leaking another tenant's history.
	_, err = api.DashboardVersions(other, "d")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.PublishDashboard(other, "d", nil, nil, "mallory")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
