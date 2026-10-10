// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedDraft(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
}

func pointerOf(t *testing.T, api *Api, ctx context.Context, token string) *int32 {
	t.Helper()
	got, err := api.DashboardsByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0].PublishedVersion
}

// A dashboard that was never published serves nothing.
func TestAFreshDashboardHasNoPublishedVersion(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)
	assert.Nil(t, pointerOf(t, api, ctx, "d"))
}

// Publish is activate: each publish moves the pointer to the version it minted.
func TestPublishMovesThePointerToTheNewVersion(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)

	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	if p := pointerOf(t, api, ctx, "d"); assert.NotNil(t, p) {
		assert.Equal(t, int32(1), *p)
	}

	_, _, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	if p := pointerOf(t, api, ctx, "d"); assert.NotNil(t, p) {
		assert.Equal(t, int32(2), *p)
	}
}

// Moving the pointer is not a draft edit: updatedAt is the draft's optimistic-concurrency
// token, and an author's next save must not conflict with their own publish.
func TestPublishLeavesTheDraftUpdatedAtUnchanged(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "d", Definition: defA})
	require.NoError(t, err)
	before := *util.FormatTime(created.UpdatedAt)

	time.Sleep(20 * time.Millisecond)
	_, dash, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", &before)
	require.NoError(t, err)

	assert.Equal(t, before, *util.FormatTime(dash.UpdatedAt), "the publish response must carry the unchanged updatedAt")
	stored, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)
	assert.Equal(t, before, *util.FormatTime(stored[0].UpdatedAt), "the stored updated_at moved on publish")

	// The author's very next save, holding the pre-publish baseline, must succeed.
	_, err = api.UpdateDashboard(ctx, "d", &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, &before)
	assert.NoError(t, err)
}

// Publish keeps the precondition it had, now checked against the locked row.
func TestStalePublishDoesNotMoveThePointer(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)

	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", strp("2000-01-01T00:00:00Z"))
	assert.ErrorIs(t, err, ErrConflict)
	assert.Nil(t, pointerOf(t, api, ctx, "d"))
}

// Activating an older version re-serves it and touches nothing of the draft.
func TestActivateReservesAnOlderVersionWithoutTouchingTheDraft(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)
	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil) // v1 = defA
	require.NoError(t, err)
	_, err = api.UpdateDashboard(ctx, "d", &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, nil)
	require.NoError(t, err)
	_, _, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil) // v2 = defB
	require.NoError(t, err)
	_, err = api.UpdateDashboard(ctx, "d", &DashboardUpdateRequest{
		Definition: util.OptionalStringOf(`{"schemaVersion":1,"widgets":[{"id":"draft-only"}]}`)}, nil)
	require.NoError(t, err)
	draftBefore, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	activated, err := api.ActivateDashboardVersion(ctx, "d", 1)
	require.NoError(t, err)
	if assert.NotNil(t, activated.PublishedVersion) {
		assert.Equal(t, int32(1), *activated.PublishedVersion)
	}

	draftAfter, err := api.DashboardsByToken(ctx, []string{"d"})
	require.NoError(t, err)
	assert.JSONEq(t, string(draftBefore[0].Definition), string(draftAfter[0].Definition), "activate rewrote the draft")
	assert.Equal(t, *util.FormatTime(draftBefore[0].UpdatedAt), *util.FormatTime(draftAfter[0].UpdatedAt), "activate moved updatedAt")
	assert.Equal(t, *util.FormatTime(draftBefore[0].UpdatedAt), *util.FormatTime(activated.UpdatedAt))

	_, served, err := api.PublishedDashboard(ctx, "d")
	require.NoError(t, err)
	assert.JSONEq(t, defA, string(served.Definition))
}

func TestActivateAVersionThatDoesNotExistIsNotFound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)
	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)

	_, err = api.ActivateDashboardVersion(ctx, "d", 99)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	if p := pointerOf(t, api, ctx, "d"); assert.NotNil(t, p) {
		assert.Equal(t, int32(1), *p, "a refused activation moved the pointer")
	}
	_, err = api.ActivateDashboardVersion(ctx, "nope", 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// The published read serves the snapshot, not the draft, and refuses a never-published
// board with a typed error rather than a blank one.
func TestPublishedDashboardServesTheSnapshotNotTheDraft(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)

	_, _, err := api.PublishedDashboard(ctx, "d")
	assert.ErrorIs(t, err, ErrNotPublished)

	_, _, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	_, err = api.UpdateDashboard(ctx, "d", &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, nil)
	require.NoError(t, err)

	dash, version, err := api.PublishedDashboard(ctx, "d")
	require.NoError(t, err)
	assert.Equal(t, "d", dash.Token)
	assert.Equal(t, int32(1), version.Version)
	assert.JSONEq(t, defA, string(version.Definition))
}

func TestPublishedDashboardUnknownTokenIsNotFound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, _, err := api.PublishedDashboard(ctx, "nope")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestDashboardVersionReadsOneSnapshotBody(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)
	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)

	v, err := api.DashboardVersion(ctx, "d", 1)
	require.NoError(t, err)
	assert.JSONEq(t, defA, string(v.Definition))
	_, err = api.DashboardVersion(ctx, "d", 2)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Tenant scope: another tenant resolves none of the new reads or writes.
func TestPublishPointerIsTenantScoped(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")
	seedDraft(t, api, acme)
	_, _, err := api.PublishDashboard(acme, "d", nil, nil, "alice", nil)
	require.NoError(t, err)

	_, _, err = api.PublishedDashboard(other, "d")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.DashboardVersion(other, "d", 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.ActivateDashboardVersion(other, "d", 1)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// The lifecycle gate covers activate like every other write.
func TestActivateIsRefusedForADeletedTenant(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDraft(t, api, ctx)
	_, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)
	_, _, err = api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	require.NoError(t, err)

	api.TenantDeleted = func(string) bool { return true }
	_, err = api.ActivateDashboardVersion(ctx, "d", 1)
	var deleted *TenantDeletedError
	assert.ErrorAs(t, err, &deleted)
}
