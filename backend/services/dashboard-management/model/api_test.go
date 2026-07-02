// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

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
	require.NoError(t, db.AutoMigrate(&Dashboard{}))
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
	})
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
	})
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
