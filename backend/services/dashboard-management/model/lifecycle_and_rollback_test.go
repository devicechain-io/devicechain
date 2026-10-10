// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"gorm.io/gorm"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defA = `{"schemaVersion":1,"widgets":[{"id":"a"}]}`
	defB = `{"schemaVersion":1,"widgets":[{"id":"b"}]}`
)

// A rollback replaces the whole draft, so a caller holding a stale updatedAt must be
// refused rather than clobber the edit another writer saved in between.
func TestRollbackWithAStalePreconditionIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	_, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	loaded := *util.FormatTime(created.UpdatedAt)

	// A concurrent writer saves; the rolling-back caller still holds `loaded`.
	_, err = api.UpdateDashboard(ctx, "d", &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, nil)
	require.NoError(t, err)

	_, err = api.RollbackDashboard(ctx, "d", 1, &loaded)
	assert.ErrorIs(t, err, ErrConflict, "a rollback from a stale view must be refused")

	got, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)
	assert.JSONEq(t, defB, string(got[0].Definition), "the concurrent edit must survive the refused rollback")

	// With the current timestamp it goes through.
	current := *util.FormatTime(got[0].UpdatedAt)
	rolled, err := api.RollbackDashboard(ctx, "d", 1, &current)
	require.NoError(t, err)
	assert.JSONEq(t, defA, string(rolled.Definition))
}

// A deleted tenant's write is refused at the lifecycle gate; another tenant's is not,
// and a delete (the erasure itself) is never gated.
func TestDeletedTenantWritesAreRefused(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(acme, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	_, err = api.PublishDashboard(acme, "d", nil, nil, "alice", nil)
	require.NoError(t, err)

	api.TenantDeleted = func(tenant string) bool { return tenant == "acme" }

	var deleted *TenantDeletedError
	_, err = api.UpdateDashboard(acme, "d", &DashboardUpdateRequest{Name: util.OptionalStringOf("x")}, nil)
	require.ErrorAs(t, err, &deleted, "update for a deleted tenant")
	_, err = api.CreateDashboard(acme, &DashboardCreateRequest{Token: "e", Definition: defA})
	assert.ErrorAs(t, err, &deleted, "create for a deleted tenant")
	_, err = api.PublishDashboard(acme, "d", nil, nil, "alice", nil)
	assert.ErrorAs(t, err, &deleted, "publish for a deleted tenant")
	_, err = api.RollbackDashboard(acme, "d", 1, nil)
	assert.ErrorAs(t, err, &deleted, "rollback for a deleted tenant")
	assert.Equal(t, "TENANT_DELETED", deleted.Extensions()["code"])

	other := core.WithTenant(context.Background(), "other")
	_, err = api.CreateDashboard(other, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err, "a live tenant is unaffected")

	ok, err := api.DeleteDashboard(acme, "d")
	require.NoError(t, err, "erasure of a deleted tenant's rows must keep working")
	assert.True(t, ok)
}

// The history is read a clamped page at a time, newest first, with no snapshot bodies.
func TestDashboardVersionsArePaginatedAndClamped(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	found, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)
	dashID := found[0].ID

	const total = rdb.MaxPageSize + 5
	rows := make([]DashboardVersion, 0, total)
	for i := 1; i <= total; i++ {
		rows = append(rows, DashboardVersion{DashboardID: dashID, Version: int32(i), Definition: []byte(defA),
			TenantScoped: rdb.TenantScoped{TenantId: "acme"}})
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(rows, 200).Error)

	huge := int32(1 << 20)
	all, err := api.DashboardVersions(ctx, "d", &huge, nil)
	require.NoError(t, err)
	assert.Len(t, all, rdb.MaxPageSize, "a limit above the maximum is clamped")
	assert.Equal(t, int32(total), all[0].Version, "newest first")
	assert.Empty(t, all[0].Definition, "snapshot bodies are not loaded")

	two, off := int32(2), int32(1)
	page, err := api.DashboardVersions(ctx, "d", &two, &off)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, int32(total-1), page[0].Version)

	def, err := api.DashboardVersions(ctx, "d", nil, nil)
	require.NoError(t, err)
	assert.Len(t, def, rdb.DefaultPageSize)
}

// A search page carries no definition bodies.
func TestDashboardSearchOmitsDefinitions(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	page, err := api.Dashboards(ctx, DashboardSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	assert.Empty(t, page.Results[0].Definition)
	assert.Equal(t, "d", page.Results[0].Token)
}

// The guarded write must itself hold against a writer that lands BETWEEN the read and the
// UPDATE: the early comparison cannot see that one. A hook injects the write at exactly
// that point, so a rollback whose UPDATE is unconditional overwrites it and fails here.
func TestRollbackWriteIsGuardedAgainstAWriterInTheGap(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	_, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	loaded := *util.FormatTime(created.UpdatedAt)

	fired := false
	const hook = "test:writer_in_the_gap"
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "dashboards" {
			return
		}
		fired = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec(
			"UPDATE dashboards SET definition = ?, updated_at = ? WHERE token = ?",
			defB, created.UpdatedAt.Add(time.Hour), "d").Error; err != nil {
			tx.AddError(err)
		}
	}))

	_, err = api.RollbackDashboard(ctx, "d", 1, &loaded)
	require.True(t, fired, "the hook never ran, so nothing was tested")
	assert.ErrorIs(t, err, ErrConflict)

	got, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)
	assert.JSONEq(t, defB, string(got[0].Definition), "the writer in the gap must survive")
}
