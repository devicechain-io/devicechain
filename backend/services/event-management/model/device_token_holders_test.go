// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// readPump1 returns every reading, and the number of base events, a read of "pump-1" by token
// returns over the fixture range.
func readPump1(t *testing.T, api *Api, ctx context.Context) (values []float64, events int) {
	t.Helper()
	token := "pump-1"
	criteria := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		DeviceToken: &token, StartTime: &reincarnationRange.start, EndTime: &reincarnationRange.end}
	m, err := api.MeasurementEvents(ctx, criteria)
	require.NoError(t, err)
	for _, r := range m.Results {
		values = append(values, r.Value.Float64)
	}
	e, err := api.Events(ctx, criteria)
	require.NoError(t, err)
	return values, len(e.Results)
}

// allPump1 is every stored reading of "pump-1", both devices', orphans included, and the
// number of its stored base events.
func allPump1() (values []float64, parents int) {
	for _, f := range reincarnationFixtures {
		if f.device != "pump-1" {
			continue
		}
		values = append(values, f.value)
		if !f.orphan {
			parents++
		}
	}
	return values, parents
}

// 🔴 THE PIN. A token deleted and never created again reads its whole history by token, exactly
// as before the bound existed: an operator auditing a removed device relies on it, and a minor
// release never changes what an existing field returns. Only a RE-CREATED token is bounded.
func TestADeletedTokenNeverCreatedAgainReadsItsHistoryUnchanged(t *testing.T) {
	api := newPersistenceTestApi(t)
	seedReincarnation(t, api, "acme")
	ctx := core.WithTenant(context.Background(), "acme")
	// The same deletion with no live holder: drop the one seedReincarnation recorded.
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "pump-1").Delete(&DeviceTokenHolder{}).Error)

	values, events := readPump1(t, api, ctx)
	wantValues, wantEvents := allPump1()
	assert.ElementsMatch(t, wantValues, values, "every reading of the deleted device, unbounded")
	assert.Equal(t, wantEvents, events, "every base event of the deleted device, unbounded")

	// A holder from BEFORE the deletion is the deleted device itself, not a re-creation.
	require.NoError(t, api.RecordDeviceTokenHeld(ctx, "pump-1", reincarnationDeletedAt.Add(-time.Hour)))
	values, _ = readPump1(t, api, ctx)
	assert.ElementsMatch(t, wantValues, values, "the deleted device's own roster fact does not bound its history")
}

// Deleting the re-created device too moves the watermark past its holder: the token is only
// deleted again, and reads its whole history, both devices', unbounded.
func TestDeletingTheReCreatedDeviceTurnsTheBoundOff(t *testing.T) {
	api := newPersistenceTestApi(t)
	seedReincarnation(t, api, "acme")
	ctx := core.WithTenant(context.Background(), "acme")
	values, _ := readPump1(t, api, ctx)
	require.ElementsMatch(t, wantReincarnation("pump-1"), values, "precondition: the re-created token is bounded")

	_, err := api.DeleteAnchorsForEntity(ctx, "device", "pump-1", reincarnationDeletedAt.Add(2*time.Hour))
	require.NoError(t, err)
	values, events := readPump1(t, api, ctx)
	wantValues, wantEvents := allPump1()
	assert.ElementsMatch(t, wantValues, values, "a re-deleted token reads its history unbounded")
	assert.Equal(t, wantEvents, events)

	// ...until it is created a third time, when nothing received before the second deletion is
	// the third device's.
	require.NoError(t, api.RecordDeviceTokenHeld(ctx, "pump-1", reincarnationDeletedAt.Add(3*time.Hour)))
	values, _ = readPump1(t, api, ctx)
	assert.Empty(t, values, "a third device sees nothing received before the second deletion")
}

// HeldSince only moves forward: an older or duplicate fact, in any order, never moves it back.
func TestRecordDeviceTokenHeldIsForwardOnly(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	at := reincarnationDeletedAt
	for _, since := range []time.Time{at, at.Add(time.Hour), at.Add(-time.Hour), at.Add(time.Hour), at.Add(30 * time.Minute)} {
		require.NoError(t, api.RecordDeviceTokenHeld(ctx, "pump-1", since))
	}
	var held []DeviceTokenHolder
	require.NoError(t, api.RDB.DB(ctx).Find(&held).Error)
	require.Len(t, held, 1)
	assert.Truef(t, held[0].HeldSince.Equal(at.Add(time.Hour)), "held since %v", held[0].HeldSince)

	// Another tenant's same token is its own row.
	other := core.WithTenant(context.Background(), "other")
	require.NoError(t, api.RecordDeviceTokenHeld(other, "pump-1", at.Add(-time.Hour)))
	var n int64
	require.NoError(t, api.RDB.DB(other).Model(&DeviceTokenHolder{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

// The sweep's page: the tenant's deleted DEVICE tokens with no holder at or after their latest
// deletion, by token, strictly after the cursor.
func TestUnheldDeletedDeviceTokensAfter(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	at := reincarnationDeletedAt
	for _, d := range []struct{ typ, token string }{
		{"device", "a-unheld"}, {"device", "b-held"}, {"device", "c-held-before"},
		{"customer", "d-customer"}, {"device", "e-unheld"},
	} {
		_, err := api.DeleteAnchorsForEntity(ctx, d.typ, d.token, at)
		require.NoError(t, err)
	}
	require.NoError(t, api.RecordDeviceTokenHeld(ctx, "b-held", at))
	require.NoError(t, api.RecordDeviceTokenHeld(ctx, "c-held-before", at.Add(-time.Second)))
	_, err := api.DeleteAnchorsForEntity(core.WithTenant(context.Background(), "other"), "device", "z-other", at)
	require.NoError(t, err)

	tokens := func(after string) []string {
		page, err := api.UnheldDeletedDeviceTokensAfter(ctx, after, time.Now())
		require.NoError(t, err)
		var out []string
		for _, d := range page {
			assert.Truef(t, d.DeletedTime.Equal(at), "%s carries its deletion, got %v", d.Token, d.DeletedTime)
			out = append(out, d.Token)
		}
		return out
	}
	assert.Equal(t, []string{"a-unheld", "c-held-before", "e-unheld"}, tokens(""))
	assert.Equal(t, []string{"e-unheld"}, tokens("c-held-before"))
	assert.Empty(t, tokens("e-unheld"))
}

// The migration owns a literal snapshot, comes right after the watermark table's, and replays.
func TestDeviceTokenHoldersMigrationSnapshot(t *testing.T) {
	assert.Equal(t, []string{
		`CREATE TABLE IF NOT EXISTS "event-management".device_token_holders (
		tenant_id varchar(128) NOT NULL,
		device_token varchar(128) NOT NULL,
		held_since timestamptz NOT NULL,
		PRIMARY KEY (tenant_id, device_token)
	)`,
	}, deviceTokenHoldersDDL, "the migration owns its literal snapshot")
	m := NewDeviceTokenHoldersSchema()
	assert.Equal(t, "20261120000000", m.ID)
	assert.Equal(t, migrationPosition(t, NewEntityDeletionWatermarksSchema().ID)+1, migrationPosition(t, m.ID))
}

// The sweep records a re-created token as held from EXACTLY the deletion it asked about. That
// bounds the token (held_since >= deleted_time), and any newer deletion, even a microsecond
// later, turns the bound off again: a deletion of the re-created device that was in flight while
// the sweep asked can never be overtaken by the sweep's evidence.
func TestAHolderAtTheDeletionItselfBoundsUntilAnyNewerDeletion(t *testing.T) {
	api := newPersistenceTestApi(t)
	seedReincarnation(t, api, "acme")
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "pump-1").Delete(&DeviceTokenHolder{}).Error)
	require.NoError(t, api.RecordDeviceTokenHeld(ctx, "pump-1", reincarnationDeletedAt))
	values, _ := readPump1(t, api, ctx)
	assert.ElementsMatch(t, wantReincarnation("pump-1"), values, "held from the deletion itself: re-created")

	_, err := api.DeleteAnchorsForEntity(ctx, "device", "pump-1", reincarnationDeletedAt.Add(time.Microsecond))
	require.NoError(t, err)
	values, _ = readPump1(t, api, ctx)
	wantValues, _ := allPump1()
	assert.ElementsMatch(t, wantValues, values, "a newer deletion overtakes the sweep's evidence")
}

// The migration runs twice over seeded rows (migrations replay from the top after a failure).
func TestDeviceTokenHoldersMigrationReplays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "event-management"`).Error)
	m := NewDeviceTokenHoldersSchema()
	require.NoError(t, m.Migrate(db))
	require.NoError(t, db.Exec(`INSERT INTO "event-management".device_token_holders
		(tenant_id, device_token, held_since) VALUES ('acme', 'pump-1', '2026-11-10')`).Error)
	require.NoError(t, m.Migrate(db), "replaying from the top must succeed")
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM "event-management".device_token_holders`).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "replay preserves holder rows")
}
