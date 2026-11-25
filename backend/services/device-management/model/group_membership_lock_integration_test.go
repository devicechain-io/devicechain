// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type membershipRaceHolder struct{}

func membershipPostgres(t *testing.T) (*Api, context.Context) {
	t.Helper()
	mgr := newPostgresRdbManager(t)
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("family-%d", time.Now().UnixNano())), 45*time.Second)
	t.Cleanup(cancel)
	pool, err := mgr.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(8) // two writers, a registrar and an independent observer
	var isolation string
	require.NoError(t, mgr.Database.WithContext(ctx).Raw("SHOW default_transaction_isolation").Scan(&isolation).Error)
	require.Equal(t, "read committed", isolation, "membership needs fresh snapshots after a lock wait")
	return NewApi(mgr), ctx
}

func membershipRaceEntity(t *testing.T, api *Api, ctx context.Context, family, token string) uint {
	t.Helper()
	switch family {
	case "device":
		_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: token + "-type"})
		require.NoError(t, err)
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: token + "-type"})
		require.NoError(t, err)
		return d.ID
	case "asset":
		_, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: token + "-type"})
		require.NoError(t, err)
		a, err := api.CreateAsset(ctx, &AssetCreateRequest{Token: token, AssetTypeToken: token + "-type"})
		require.NoError(t, err)
		return a.ID
	case "customer":
		_, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: token + "-type"})
		require.NoError(t, err)
		c, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: token, CustomerTypeToken: token + "-type"})
		require.NoError(t, err)
		return c.ID
	case "area":
		_, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: token + "-type"})
		require.NoError(t, err)
		a, err := api.CreateArea(ctx, &AreaCreateRequest{Token: token, AreaTypeToken: token + "-type"})
		require.NoError(t, err)
		return a.ID
	default:
		t.Fatalf("unsupported fixture family %q", family)
		return 0
	}
}

func membershipRaceGroup(t *testing.T, api *Api, ctx context.Context, family string) (*EntityGroup, int32) {
	t.Helper()
	mode, source := string(MembershipDynamic), `attr["a"] == "x" && attr["b"] == "y"`
	g, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{Token: "group", MemberType: family, MembershipMode: &mode, Selector: &source})
	require.NoError(t, err)
	v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
	require.NoError(t, err)
	return g, v.Version
}

func membershipRaceWrite(api *Api, ctx context.Context, family, token, key, value string) error {
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: family, Entity: token,
		Scope: "SHARED", AttrKey: key, ValueType: "STRING", Value: &value})
	return err
}

// Park AFTER the real holder has evaluated its selector. This changes no SQL and
// bypasses no lock: both facet writes and registration use the public API.
func parkMembershipHolder(t *testing.T, api *Api, registration bool) (<-chan int, func()) {
	t.Helper()
	ready, release := make(chan int, 1), make(chan struct{})
	var once, parked sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	park := func(db *gorm.DB) {
		if db.Error != nil || db.Statement.Context.Value(membershipRaceHolder{}) != true {
			return
		}
		if registration {
			if _, ok := db.Statement.Dest.(*[]EntityMember); !ok {
				return
			}
		} else if db.Statement.Table != "entity_group_memberships" {
			return
		}
		parked.Do(func() {
			var pid int
			if err := db.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				db.AddError(err)
				return
			}
			ready <- pid
			select {
			case <-release:
			case <-db.Statement.Context.Done():
				db.AddError(db.Statement.Context.Err())
			}
		})
	}
	if registration {
		require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("test:membership-holder", park))
		t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove("test:membership-holder") })
	} else {
		require.NoError(t, api.RDB.Database.Callback().Delete().After("gorm:delete").Register("test:membership-holder", park))
		t.Cleanup(func() { api.RDB.Database.Callback().Delete().Remove("test:membership-holder") })
	}
	t.Cleanup(unblock)
	return ready, unblock
}

func membershipHolderPID(t *testing.T, ctx context.Context, ready <-chan int, done <-chan error) int {
	t.Helper()
	select {
	case pid := <-ready:
		return pid
	case err := <-done:
		t.Fatalf("holder finished before overlap: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return 0
}

func membershipWriterDone(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// Match the holder's precise advisory key and backend, so an unrelated lock wait
// elsewhere in the server cannot satisfy the synchronization.
func membershipWaiters(db *gorm.DB, pid int) (int64, error) {
	var waiting int64
	err := db.Raw(`SELECT count(*) FROM pg_locks w JOIN pg_locks h
		ON w.locktype = h.locktype AND w.database = h.database
		AND w.classid = h.classid AND w.objid = h.objid AND w.objsubid = h.objsubid
		WHERE h.pid = ? AND h.locktype = 'advisory' AND h.granted AND NOT w.granted`, pid).Scan(&waiting).Error
	return waiting, err
}

func TestMembershipFamilyConcurrentWritesPostgres(t *testing.T) {
	for _, family := range []string{"device", "asset", "customer", "area"} {
		t.Run(family, func(t *testing.T) { testMembershipFamilyRace(t, family, false) })
	}
}

func TestMembershipFamilyWriteRacingRegistrationPostgres(t *testing.T) {
	for _, family := range []string{"device", "asset", "customer", "area"} {
		t.Run(family, func(t *testing.T) { testMembershipFamilyRace(t, family, true) })
	}
}

// The holder sees false, then the second facet is written on another connection.
// With a non-device shared-lock mutant, that writer commits an incomplete result
// before the holder. We let both commit and compare membership with the committed
// facets, so the failure proves write skew rather than just lock mode.
func testMembershipFamilyRace(t *testing.T, family string, registration bool) {
	t.Helper()
	api, ctx := membershipPostgres(t)
	id := membershipRaceEntity(t, api, ctx, family, "member")
	g, version := membershipRaceGroup(t, api, ctx, family)
	if registration {
		require.NoError(t, membershipRaceWrite(api, ctx, family, "member", "a", "x"))
	} else {
		require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, version))
	}
	ready, unblock := parkMembershipHolder(t, api, registration)
	var workers sync.WaitGroup
	defer func() { unblock(); workers.Wait() }()
	holderDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		holderCtx := context.WithValue(ctx, membershipRaceHolder{}, true)
		if registration {
			holderDone <- api.RegisterGroupVersionForScoping(holderCtx, g.Token, version)
		} else {
			holderDone <- membershipRaceWrite(api, holderCtx, family, "member", "a", "x")
		}
	}()
	pid := membershipHolderPID(t, ctx, ready, holderDone)
	done := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		done <- membershipRaceWrite(api, ctx, family, "member", "b", "y")
	}()
	finished := false
	var writerErr error
	require.Eventually(t, func() bool {
		select {
		case err := <-done:
			writerErr = err
			finished = true
			return true
		default:
		}
		n, err := membershipWaiters(api.RDB.Database.WithContext(ctx), pid)
		return err == nil && n > 0
	}, 10*time.Second, 10*time.Millisecond, "writer must block or expose shared-mode write skew")
	require.NoError(t, writerErr)
	unblock()
	membershipWriterDone(t, ctx, holderDone)
	if !finished {
		membershipWriterDone(t, ctx, done)
	}
	var attrs []EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ? AND scope = ?", family, id, "SHARED").Find(&attrs).Error)
	values := map[string]string{}
	for _, attr := range attrs {
		values[attr.AttrKey] = attr.Value.String
	}
	require.Equal(t, "x", values["a"])
	require.Equal(t, "y", values["b"])
	require.True(t, isMemberOf(t, api, ctx, family, id, g.ID, version), "membership must match BOTH committed facets (finished before holder: %t)", finished)
	// A solitary writer is the counterweight: an ordinary leave still works.
	require.NoError(t, membershipRaceWrite(api, ctx, family, "member", "b", "other"))
	require.False(t, isMemberOf(t, api, ctx, family, id, g.ID, version))
}
