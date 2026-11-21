// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type deleteRaceRole struct{}

// Park only after the real statement acquired its locks, on its own connection.
// Context deadlines and the deferred release also unwind a failed assertion.
func parkDeleteRace(t *testing.T, api *Api, ctx context.Context, kind, role string, matches func(*gorm.DB) bool) (<-chan int, func()) {
	t.Helper()
	held, release := make(chan int, 1), make(chan struct{})
	var parked, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	callback := func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Context.Value(deleteRaceRole{}) != role || !matches(tx) {
			return
		}
		parked.Do(func() {
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				tx.AddError(err)
				return
			}
			held <- pid
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		})
	}
	name := "test:delete-race-" + role
	if kind == "query" {
		require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register(name, callback))
		t.Cleanup(func() { _ = api.RDB.Database.Callback().Query().Remove(name) })
	} else {
		require.NoError(t, api.RDB.Database.Callback().Create().After("gorm:create").Register(name, callback))
		t.Cleanup(func() { _ = api.RDB.Database.Callback().Create().Remove(name) })
	}
	t.Cleanup(unblock)
	return held, unblock
}

func deleteRacePID(t *testing.T, ctx context.Context, held <-chan int) int {
	t.Helper()
	select {
	case pid := <-held:
		return pid
	case <-ctx.Done():
		t.Fatal("writer never reached its lock barrier")
		return 0
	}
}

// Match a particular blocker, not a waiter from another tenant's test.
func deleteRaceWaiter(db *gorm.DB, blocker int, query string) int {
	var pid int
	_ = db.Raw(`SELECT pid FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))
		AND query ILIKE ? LIMIT 1`, blocker, query).Scan(&pid).Error
	return pid
}

func requireNoDeleteDeadlock(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		t.Fatalf("pre-existing delete lock cycle reproduced (40P01): %v", err)
	}
}

// On main: Create holds G; Delete holds M(G) and waits G; Set holds F and
// waits M(G). Releasing Create to enroll closes the real three-connection cycle.
// With the fix Delete waits G before touching M(G), so Set can finish first.
func TestGatewayCreateDeleteAttributeDoNotDeadlockPostgres(t *testing.T) {
	api, ctx, ids := ownershipPgRig(t, 1)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	g := createDynamicGroup(t, api, ctx, "unretired", `!("retired" in attr)`)
	v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
	require.NoError(t, err)
	require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
	members, err := api.MembershipsForEntity(ctx, "device", ids[0])
	require.NoError(t, err)
	require.Len(t, members, 1)
	held, release := parkDeleteRace(t, api, ctx, "query", "create", func(tx *gorm.DB) bool {
		return tx.Statement.Table == "devices" && strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE")
	})
	defer release()
	created := make(chan error, 1)
	go func() {
		_, err := api.CreateDevice(context.WithValue(ctx, deleteRaceRole{}, "create"), &DeviceCreateRequest{
			Token: "child", DeviceTypeToken: "pump", GatewayToken: strp("pump-00000")})
		created <- err
	}()
	createPID := deleteRacePID(t, ctx, held)
	deleted := make(chan error, 1)
	go func() { _, err := api.DeleteDevice(ctx, "pump-00000"); deleted <- err }()
	var deletePID int
	require.Eventually(t, func() bool {
		deletePID = deleteRaceWaiter(api.RDB.DB(ctx), createPID, "%devices%FOR UPDATE%")
		return deletePID != 0
	}, 5*time.Second, 10*time.Millisecond, "delete never waited for the gateway")
	written := make(chan error, 1)
	go func() {
		_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "pump-00000",
			Scope: "SHARED", AttrKey: "retired", ValueType: "STRING", Value: strp("yes")})
		written <- err
	}()
	finished := false
	var writeErr error
	require.Eventually(t, func() bool {
		select {
		case writeErr = <-written:
			finished = true
			return true
		default:
			// The writer now queues on the device attribute lock the delete took first;
			// before that lock existed it reached the membership rows the delete held.
			db := api.RDB.DB(ctx)
			return deleteRaceWaiter(db, deletePID, "%entity_group_memberships%") != 0 ||
				deleteRaceWaiter(db, deletePID, "%dc_entity_attr_lock%") != 0
		}
	}, 5*time.Second, 10*time.Millisecond, "attribute writer neither finished nor waited on membership or the device attribute lock")
	release()
	createErr, deleteErr := <-created, <-deleted
	if !finished {
		writeErr = <-written
	}
	for _, err := range []error{createErr, deleteErr, writeErr} {
		requireNoDeleteDeadlock(t, err)
	}
	require.NoError(t, createErr)
	require.NoError(t, writeErr)
	require.ErrorIs(t, deleteErr, ErrGatewayHasChildren)
}

// main's sequential-scan DELETE visits b before a. Restore has already written
// a; once deletion waits on a, restore proceeds to b. PostgreSQL must detect
// 40P01 on main. Sorted deletes instead wait on a without owning b.
func TestConfigurationRestoreAndDeleteDoNotDeadlockPostgres(t *testing.T) {
	api, ctx, _ := ownershipPgRig(t, 0)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	seedDeclaredDevice(t, api, ctx, "profile", "victim", []ConfigurationKey{{Key: "a", ValueType: "STRING"}, {Key: "b", ValueType: "STRING"}})
	setShared(t, api, ctx, "victim", "b", AttributeValueType("STRING"), "old")
	setShared(t, api, ctx, "victim", "a", AttributeValueType("STRING"), "old")
	id, err := api.ResolveEntityToken(ctx, "device", "victim")
	require.NoError(t, err)
	var physical []string
	require.NoError(t, api.RDB.DB(ctx).Raw(`SELECT attr_key FROM entity_attributes WHERE entity_id = ? AND tenant_id = ? ORDER BY ctid`, id, tenantOf(ctx)).Scan(&physical).Error)
	require.Equal(t, []string{"b", "a"}, physical)
	held, release := parkDeleteRace(t, api, ctx, "create", "restore", func(tx *gorm.DB) bool {
		row, ok := tx.Statement.Dest.(*EntityAttribute)
		return ok && row.AttrKey == "a"
	})
	defer release()
	// Force the unsafe bulk delete's physical order independently of planner cost.
	require.NoError(t, api.RDB.Database.Callback().Delete().Before("gorm:delete").Register("test:physical-delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_attributes" {
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("SET LOCAL enable_indexscan = off").Error)
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("SET LOCAL enable_bitmapscan = off").Error)
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Delete().Remove("test:physical-delete") })
	restored := make(chan error, 1)
	go func() {
		restored <- api.applyConfigurationDocument(context.WithValue(ctx, deleteRaceRole{}, "restore"), "victim", id, `{"a":"new","b":"new"}`, "restore")
	}()
	restorePID := deleteRacePID(t, ctx, held)
	deleted := make(chan error, 1)
	go func() { _, err := api.DeleteDevice(ctx, "victim"); deleted <- err }()
	// Deletion now takes the device's attribute lock first, so it queues behind restore
	// there, before it can own any attribute row: the cycle this test guards cannot form.
	require.Eventually(t, func() bool {
		return deleteRaceWaiter(api.RDB.DB(ctx), restorePID, "%dc_entity_attr_lock%") != 0
	}, 5*time.Second, 10*time.Millisecond, "delete never waited for restore's device attribute lock")
	release()
	restoreErr, deleteErr := <-restored, <-deleted
	requireNoDeleteDeadlock(t, restoreErr)
	requireNoDeleteDeadlock(t, deleteErr)
	require.NoError(t, restoreErr)
	require.NoError(t, deleteErr)
}
