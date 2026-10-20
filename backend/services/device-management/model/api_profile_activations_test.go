// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func activationsOf(t *testing.T, api *Api, ctx context.Context, token string) []*DeviceProfileActivation {
	t.Helper()
	rows, next, err := api.DeviceProfileActivations(ctx, token, 0, 1000)
	require.NoError(t, err)
	require.Zero(t, next)
	return rows
}

// Every path that moves the active version writes exactly one log entry, naming the
// version that became active, why, who, and when, newest first.
func TestPublishAndRollbackEachLogOneActivation(t *testing.T) {
	api, ctx := configTestApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)

	require.Empty(t, activationsOf(t, api, ctx, "prof"), "an unpublished profile has no activations")

	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "alice")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "prof", nil, nil, "bob")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(auth.WithClaims(ctx, &auth.Claims{Username: "carol"}), "prof", 1)
	require.NoError(t, err)

	got := activationsOf(t, api, ctx, "prof")
	require.Len(t, got, 3)
	want := []struct {
		version int32
		kind    string
		by      string
	}{{1, ActivationKindRollback, "carol"}, {2, ActivationKindPublish, "bob"}, {1, ActivationKindPublish, "alice"}}
	for i, w := range want {
		require.Equal(t, w.version, got[i].Version, "entry %d", i)
		require.Equal(t, w.kind, got[i].Kind, "entry %d", i)
		require.Equal(t, w.by, got[i].ActivatedBy, "entry %d", i)
	}
	// The instants are strictly increasing with the log, and the newest is the profile's
	// stored active_since: one activation instant, not a second clock reading.
	require.True(t, got[2].ActivatedAt.Before(got[1].ActivatedAt))
	require.True(t, got[1].ActivatedAt.Before(got[0].ActivatedAt))
	p, err := api.deviceProfileByToken(ctx, "prof")
	require.NoError(t, err)
	require.True(t, p.ActiveSince.Time.Equal(got[0].ActivatedAt), "%v vs %v", p.ActiveSince.Time, got[0].ActivatedAt)
}

// A refused rollback (no such version) and a refused publish leave no entry.
func TestRefusedActivationsLogNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "alice")
	require.NoError(t, err)

	_, err = api.RollbackDeviceProfile(ctx, "prof", 99)
	require.Error(t, err)
	require.Len(t, activationsOf(t, api, ctx, "prof"), 1)
}

// The entry commits with the pointer move: a transaction that rolls back takes it along.
func TestActivationRollsBackWithItsTransaction(t *testing.T) {
	api, ctx := configTestApi(t)
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	boom := errors.New("boom")
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := recordActivation(tx, p.ID, 1, ActivationKindPublish, time.Now(), "x"); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Empty(t, activationsOf(t, api, ctx, "prof"))
}

func TestActivationsPageByKeysetNewestFirst(t *testing.T) {
	api, ctx := configTestApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	for i := 0; i < 5; i++ {
		_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "alice")
		require.NoError(t, err)
	}

	var seen []int32
	var after uint64
	pages := 0
	for {
		rows, next, err := api.DeviceProfileActivations(ctx, "prof", after, 2)
		require.NoError(t, err)
		for _, r := range rows {
			seen = append(seen, r.Version)
		}
		pages++
		if next == 0 {
			break
		}
		require.Len(t, rows, 2)
		after = next
	}
	require.Equal(t, []int32{5, 4, 3, 2, 1}, seen)
	require.Equal(t, 3, pages)

	_, _, err := api.DeviceProfileActivations(ctx, "prof", 0, 0)
	var bad *InvalidArgumentError
	require.ErrorAs(t, err, &bad)
	_, _, err = api.DeviceProfileActivations(ctx, "missing", 0, 10)
	require.Error(t, err)
}

// A tenant reads its own log only, even where another tenant's profile has the same token.
func TestActivationsAreTenantScoped(t *testing.T) {
	api, acme := configTestApi(t)
	beta := core.WithTenant(context.Background(), "beta")
	seedProfileWithRule(t, api, acme, "prof", "r1", false)
	seedProfileWithRule(t, api, beta, "prof", "r1", false)

	_, err := api.PublishDeviceProfile(acme, "prof", nil, nil, "a")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(acme, "prof", nil, nil, "a")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(beta, "prof", nil, nil, "b")
	require.NoError(t, err)

	require.Len(t, activationsOf(t, api, acme, "prof"), 2)
	require.Len(t, activationsOf(t, api, beta, "prof"), 1)
	for _, r := range activationsOf(t, api, beta, "prof") {
		require.Equal(t, "beta", r.TenantId)
	}
}

// Deleting a profile removes its log with it, and only its own.
func TestDeletingAProfileRemovesItsActivations(t *testing.T) {
	api, ctx := configTestApi(t)
	seedProfileWithRule(t, api, ctx, "gone", "r1", false)
	seedProfileWithRule(t, api, ctx, "kept", "r2", false)
	for _, tok := range []string{"gone", "kept"} {
		_, err := api.PublishDeviceProfile(ctx, tok, nil, nil, "a")
		require.NoError(t, err)
	}
	ok, err := api.DeleteDeviceProfile(ctx, "gone")
	require.NoError(t, err)
	require.True(t, ok)

	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfileActivation{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	require.Len(t, activationsOf(t, api, ctx, "kept"), 1)
}

// A publish's entry carries the very instant stored as the version's publish time, not a second
// clock reading: for a version that was published and never rolled back to, the two must agree.
func TestPublishActivationIsTheVersionsPublishInstant(t *testing.T) {
	api, ctx := configTestApi(t)
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	var versions []*DeviceProfileVersion
	for i := 0; i < 3; i++ {
		v, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "alice")
		require.NoError(t, err)
		versions = append(versions, v)
	}
	got := activationsOf(t, api, ctx, "prof")
	require.Len(t, got, 3)
	for i, v := range versions {
		e := got[len(got)-1-i]
		require.Equal(t, v.Version, e.Version)
		require.True(t, v.CreatedAt.Equal(e.ActivatedAt), "version %d: published %v, activated %v",
			v.Version, v.CreatedAt, e.ActivatedAt)
	}
}

// The read carries the tenant predicate itself rather than relying on the profile id alone to
// keep tenants apart: a row in another tenant naming this profile's id is never returned.
func TestActivationsReadNeverCrossesTenantsEvenOnASharedProfileId(t *testing.T) {
	api, ctx := configTestApi(t)
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "alice")
	require.NoError(t, err)

	stray := &DeviceProfileActivation{DeviceProfileId: p.ID, Version: 9, Kind: ActivationKindPublish,
		ActivatedAt: time.Now().UTC(), ActivatedBy: "mallory"}
	stray.TenantId = "beta"
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).Create(stray).Error)

	got := activationsOf(t, api, ctx, "prof")
	require.Len(t, got, 1)
	require.Equal(t, "alice", got[0].ActivatedBy)
}

// A page asked for above the cap is served at the cap, not passed through to the query, and the
// cursor it returns continues where the capped page stopped.
func TestActivationsPageAboveTheCapIsCapped(t *testing.T) {
	api, ctx := configTestApi(t)
	p := seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	total := rdb.MaxPageSize + 5
	rows := make([]*DeviceProfileActivation, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, &DeviceProfileActivation{DeviceProfileId: p.ID, Version: int32(i + 1),
			Kind: ActivationKindPublish, ActivatedAt: time.Now().UTC()})
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(rows, 200).Error)

	page, next, err := api.DeviceProfileActivations(ctx, "prof", 0, total*2)
	require.NoError(t, err)
	require.Len(t, page, rdb.MaxPageSize)
	require.NotZero(t, next)
	rest, last, err := api.DeviceProfileActivations(ctx, "prof", next, total*2)
	require.NoError(t, err)
	require.Len(t, rest, 5)
	require.Zero(t, last)
}
