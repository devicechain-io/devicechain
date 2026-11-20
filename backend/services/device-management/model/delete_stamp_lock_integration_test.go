// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// syncRoster records roster facts from more than one goroutine.
type syncRoster struct {
	mu     sync.Mutex
	events []*DeviceRosterEvent
}

func (c *syncRoster) PublishDeviceRoster(_ context.Context, e *DeviceRosterEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *syncRoster) forToken(token string) []*DeviceRosterEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*DeviceRosterEvent, 0)
	for _, e := range c.events {
		if e.DeviceToken == token {
			out = append(out, e)
		}
	}
	return out
}

// raceAtTheStamp arms the delete's clock so that the moment it stamps the deletion, before the
// stamp is returned to it, writer runs on another connection. It returns the time the delete
// was stamped with, read once the delete has returned. The writer is given until it either
// finishes or is seen blocked on a lock in a statement matching waitingOn (an ILIKE pattern): a
// writer the delete has locked out waits for its commit, and the delete must not be held
// behind it. The statement is matched, and must be blocked BY someone, so that a waiter from
// another test sharing the database cannot release the delete before the writer got there.
func raceAtTheStamp(t *testing.T, api *Api, waitingOn string, writer func() error) (stamp func() time.Time, writerErr func() error) {
	t.Helper()
	var (
		once     sync.Once
		stamped  time.Time
		done     = make(chan error, 1)
		finished bool
		result   error
	)
	api.ownershipNow = func() time.Time {
		now := time.Now().UTC()
		once.Do(func() {
			stamped = now
			// The writer's own clock must read strictly later than the stamp.
			time.Sleep(2 * time.Millisecond)
			go func() { done <- writer() }()
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case err := <-done:
					finished, result = true, err
					return
				default:
				}
				var waiting int64
				err := api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
					Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
						AND wait_event_type = 'Lock' AND query ILIKE ? AND cardinality(pg_blocking_pids(pid)) > 0`,
						waitingOn).Scan(&waiting).Error
				if err == nil && waiting > 0 {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			var waiters []string
			_ = api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
				Raw(`SELECT wait_event_type || ' ' || coalesce(wait_event, '') || ' ' || query FROM pg_stat_activity
					WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiters).Error
			t.Errorf("the concurrent writer neither finished nor waited on a lock in %q; lock waiters: %q", waitingOn, waiters)
		})
		return now
	}
	t.Cleanup(func() { api.ownershipNow = nil })
	return func() time.Time { return stamped }, func() error {
		if !finished {
			select {
			case result = <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("the concurrent writer never returned")
			}
			finished = true
		}
		return result
	}
}

// deletedTimeOf is the deletion time the delete committed for token.
func deletedTimeOf(t *testing.T, api *Api, ctx context.Context, etype, token string) time.Time {
	t.Helper()
	var rows []PendingEntityDeletion
	require.NoError(t, api.RDB.DB(ctx).Where("entity_type = ? AND entity_token = ?", etype, token).Find(&rows).Error)
	require.Len(t, rows, 1, "the delete queued no single deletion fact")
	return rows[0].DeletedTime
}

// 🔴 A DELETE TAKES ITS ROW BEFORE IT STAMPS THE DELETION. event-management reads a roster fact
// newer than a token's deletion as proof the token was re-created, and drops the deleted
// device's history from reads by that token. A writer that commits a roster change to the device
// after the deletion was stamped but before the delete reached the row would date that fact past
// the deletion of a device nobody re-created. With the row locked first, the writer either
// committed before the stamp was taken (its time is earlier, on the same clock) or finds no row
// and emits nothing.
func TestADeviceDeleteLocksTheDeviceBeforeItStampsTheDeletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waiting string
		writer  func(api *Api, ctx context.Context, token string) error
	}{
		{"a disable", `UPDATE %"devices" SET%`, func(api *Api, ctx context.Context, token string) error {
			_, err := api.UpdateDevice(ctx, token, &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
			return err
		}},
		{"a type re-point", `UPDATE %"devices" SET%`, func(api *Api, ctx context.Context, _ string) error {
			_, err := api.UpdateDeviceType(ctx, "pump", &DeviceTypeUpdateRequest{
				ProfileToken: dcgraphql.OptionalString{Set: true, Value: strp("other-profile")}})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, _ := ownershipPgRig(t, 1)
			_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "other-profile"})
			require.NoError(t, err)
			roster := &syncRoster{}
			api.DeviceRosterPublisher = roster
			token := "pump-00000"

			stamp, writerErr := raceAtTheStamp(t, api, tc.waiting, func() error { return tc.writer(api, ctx, token) })
			deleted, err := api.DeleteDevice(ctx, token)
			require.NoError(t, err)
			require.True(t, deleted)
			werr := writerErr()
			require.False(t, stamp().IsZero(), "the delete never stamped its deletion")
			deletedTime := deletedTimeOf(t, api, ctx, "device", token)
			require.WithinDuration(t, stamp(), deletedTime, time.Microsecond, "the fact is not dated by the stamp")

			// The invariant itself: no roster fact for the token is newer than its deletion.
			for _, fact := range roster.forToken(token) {
				require.False(t, fact.ExpectedSince.After(deletedTime),
					"a roster fact dated %s outlives a deletion stamped %s: the device was changed after its deletion was stamped",
					fact.ExpectedSince, deletedTime)
			}
			// And how it holds here: the writer started after the stamp, so it waited for the
			// delete and found no device — the disable has nothing to update, and the re-point
			// moves a type with no devices left, so neither rosters the token.
			if tc.name == "a disable" {
				require.True(t, errors.Is(werr, gorm.ErrRecordNotFound), "the disable was not locked out: %v", werr)
			} else {
				require.NoError(t, werr)
			}
			require.Empty(t, roster.forToken(token), "a writer locked out by the delete still rostered the device")
		})
	}
}

// The same holds for every entity the edge delete handles: once the deletion is stamped, the
// entity's row is the delete's, so no writer can still change it.
func TestEveryEdgeDeleteHoldsItsRowWhenItStampsTheDeletion(t *testing.T) {
	type edgeCase struct {
		etype  string
		table  string
		create func(api *Api, ctx context.Context) error
		del    func(api *Api, ctx context.Context) (bool, error)
	}
	cases := []edgeCase{
		{"device", "devices", func(*Api, context.Context) error { return nil },
			func(api *Api, ctx context.Context) (bool, error) { return api.DeleteDevice(ctx, "pump-00000") }},
		{"customer", "customers", func(api *Api, ctx context.Context) error {
			if _, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: "ct"}); err != nil {
				return err
			}
			_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: "victim", CustomerTypeToken: "ct"})
			return err
		}, func(api *Api, ctx context.Context) (bool, error) { return api.DeleteCustomer(ctx, "victim") }},
		{"area", "areas", func(api *Api, ctx context.Context) error {
			if _, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "at"}); err != nil {
				return err
			}
			_, err := api.CreateArea(ctx, &AreaCreateRequest{Token: "victim", AreaTypeToken: "at"})
			return err
		}, func(api *Api, ctx context.Context) (bool, error) { return api.DeleteArea(ctx, "victim") }},
		{"asset", "assets", func(api *Api, ctx context.Context) error {
			if _, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "st"}); err != nil {
				return err
			}
			_, err := api.CreateAsset(ctx, &AssetCreateRequest{Token: "victim", AssetTypeToken: "st"})
			return err
		}, func(api *Api, ctx context.Context) (bool, error) { return api.DeleteAsset(ctx, "victim") }},
		{"group", "entity_groups", func(api *Api, ctx context.Context) error {
			_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "victim", MemberType: "device"})
			return err
		}, func(api *Api, ctx context.Context) (bool, error) { return api.DeleteEntityGroup(ctx, "victim") }},
	}
	for _, tc := range cases {
		t.Run(tc.etype, func(t *testing.T) {
			api, ctx, _ := ownershipPgRig(t, 1)
			require.NoError(t, tc.create(api, ctx))
			token := "victim"
			if tc.etype == "device" {
				token = "pump-00000"
			}
			id, err := api.ResolveEntityToken(ctx, tc.etype, token)
			require.NoError(t, err)
			// The writer: takes the entity's row for an update, refusing at once rather than
			// queueing behind the delete.
			// NOWAIT never waits, so nothing matches the pattern: the hook always waits for it.
			_, writerErr := raceAtTheStamp(t, api, "%FOR UPDATE NOWAIT%", func() error {
				var ids []uint
				return api.RDB.DB(ctx).Raw(fmt.Sprintf(`SELECT id FROM %q WHERE id = ? FOR UPDATE NOWAIT`, tc.table), id).
					Scan(&ids).Error
			})
			deleted, err := tc.del(api, ctx)
			require.NoError(t, err)
			require.True(t, deleted)
			werr := writerErr()
			var pgErr *pgconn.PgError
			require.True(t, errors.As(werr, &pgErr) && pgErr.Code == "55P03",
				"the %s row was writable after its deletion was stamped (writer returned %v)", tc.etype, werr)
		})
	}
}

// 🔴 A GROUP DELETE STILL TAKES THE MEMBERSHIP FAMILY BEFORE THE GROUP ROW. The row lock the
// stamp waits for is taken where the delete always took the row, after the cascade's family
// lock. Taken at the top of the transaction instead, it put the row ahead of the family, while
// an access-scope switch takes the family and then the row: a cycle the engine resolves by
// failing one of the two. Here the switch starts the moment the delete has stamped, and both
// must go through.
func TestAGroupDeleteKeepsTheFamilyLockAheadOfTheGroupRow(t *testing.T) {
	api, ctx, _ := ownershipPgRig(t, 1)
	dynamic, selector := "dynamic", `attr["region"] == "north"`
	_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "north",
		MemberType: "device", MembershipMode: &dynamic, Selector: &selector})
	require.NoError(t, err)
	_, err = api.PublishEntityGroup(ctx, "north", nil, nil, "tester")
	require.NoError(t, err)

	_, writerErr := raceAtTheStamp(t, api, "%pg_advisory_xact_lock%", func() error {
		_, err := api.SetEntityGroupAccessScope(ctx, "north", true)
		return err
	})
	deleted, err := api.DeleteEntityGroup(ctx, "north")
	require.NoError(t, err, "the group delete was failed")
	require.True(t, deleted)
	werr := writerErr()
	require.True(t, werr == nil || errors.Is(werr, gorm.ErrRecordNotFound),
		"the access-scope switch was failed: %v", werr)
}
