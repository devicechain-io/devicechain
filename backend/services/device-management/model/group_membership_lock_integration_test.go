// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type membershipRaceHolder struct{}

// Each round forces an uncommitted holder and a second writer on separate
// connections. Without the family lock both see an incomplete selector, although
// the committed attributes satisfy it. This avoids depending on scheduler luck.
func TestMembershipFamilyConcurrentWritesPostgres(t *testing.T) {
	testMembershipFamilyRace(t, false)
}

func TestMembershipFamilyWriteRacingRegistrationPostgres(t *testing.T) {
	testMembershipFamilyRace(t, true)
}

func testMembershipFamilyRace(t *testing.T, registration bool) {
	t.Helper()
	for _, bypass := range []bool{false, true} {
		t.Run(fmt.Sprintf("bypass=%t", bypass), func(t *testing.T) {
			mgr := newPostgresRdbManager(t)
			api := NewApi(mgr)
			ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("family-%d", time.Now().UnixNano())), 90*time.Second)
			defer cancel()
			pool, err := mgr.Database.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(8)
			var isolation string
			require.NoError(t, mgr.Database.WithContext(ctx).Raw("SHOW default_transaction_isolation").Scan(&isolation).Error)
			require.Equal(t, "read committed", isolation, "family locking requires fresh snapshots after a lock wait")
			_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
			require.NoError(t, err)
			// Test-only seam: preserve the real attribute/registration paths, removing
			// only the advisory locks. Never change production code for this control.
			// Both writers address ONE device, so the per-device attribute lock
			// (dc_entity_attr_lock) serializes them too and must be removed as well, or
			// the control could never expose the race the family lock exists for.
			if bypass {
				require.NoError(t, mgr.Database.Callback().Raw().Before("gorm:raw").Register("test:bypass-family", func(db *gorm.DB) {
					if sql := db.Statement.SQL.String(); strings.Contains(sql, "pg_advisory_xact_lock") || strings.Contains(sql, "dc_entity_attr_lock") {
						db.Statement.SQL.Reset()
						db.Statement.SQL.WriteString("SELECT 1")
						db.Statement.Vars = nil
					}
				}))
				defer mgr.Database.Callback().Raw().Remove("test:bypass-family")
			}
			diverged := 0
			for round := 0; round < 50; round++ {
				func() {
					ctx, cancelRound := context.WithTimeout(ctx, 15*time.Second)
					defer cancelRound()
					token := fmt.Sprintf("device-%d", round)
					device, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "sensor"})
					require.NoError(t, err)
					g := createDynamicGroup(t, api, ctx, fmt.Sprintf("group-%d", round), `attr["a"] == "x" && attr["b"] == "y"`)
					v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
					require.NoError(t, err)
					if !registration {
						require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
					}
					x := "x"
					attr := &EntityAttribute{EntityType: "device", EntityId: device.ID, Scope: "SHARED", AttrKey: "a", ValueType: "STRING", Value: rdb.NullStrOf(&x)}
					if registration {
						// a is already committed; the race is registration's empty
						// backfill versus the final b write, before refs commit.
						require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
							markAsProtocolWriter(t, tx)
							return tx.Create(attr).Error
						}))
					}
					ready, release := make(chan int, 1), make(chan struct{})
					var once sync.Once
					var parked sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					park := func(db *gorm.DB) {
						if db.Statement.Context.Value(membershipRaceHolder{}) != true || db.Error != nil {
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
							case <-ctx.Done():
								db.AddError(ctx.Err())
							}
						})
					}
					if registration {
						require.NoError(t, mgr.Database.Callback().Query().After("gorm:query").Register("test:park-holder", park))
						defer mgr.Database.Callback().Query().Remove("test:park-holder")
					} else {
						require.NoError(t, mgr.Database.Callback().Delete().After("gorm:delete").Register("test:park-holder", park))
						defer mgr.Database.Callback().Delete().Remove("test:park-holder")
					}
					holderDone := make(chan error, 1)
					var workers sync.WaitGroup
					defer func() {
						unblock()
						cancelRound()
						workers.Wait()
					}()
					workers.Add(1)
					go func() {
						defer workers.Done()
						holderCtx := context.WithValue(ctx, membershipRaceHolder{}, true)
						if registration {
							holderDone <- api.RegisterGroupVersionForScoping(holderCtx, g.Token, v.Version)
						} else {
							_, err := api.SetEntityAttribute(holderCtx, &EntityAttributeSetRequest{EntityType: "device", Entity: token,
								Scope: "SHARED", AttrKey: "a", ValueType: "STRING", Value: &x})
							holderDone <- err
						}
					}()
					var pid int
					select {
					case pid = <-ready:
					case err := <-holderDone:
						t.Fatalf("holder finished before overlap: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					// The holder has evaluated false and cannot yet commit. The
					// actual public write must either wait for it or (control)
					// commit a stale membership before the holder is released.
					done := make(chan error, 1)
					writerCtx, stopWriter := context.WithTimeout(ctx, 10*time.Second)
					defer stopWriter()
					workers.Add(1)
					go func() {
						defer workers.Done()
						y := "y"
						_, err := api.SetEntityAttribute(writerCtx, &EntityAttributeSetRequest{EntityType: "device", Entity: token,
							Scope: "SHARED", AttrKey: "b", ValueType: "STRING", Value: &y})
						done <- err
					}()
					if bypass {
						select {
						case err := <-done:
							require.NoError(t, err)
						case <-writerCtx.Done():
							t.Fatal("unlocked writer failed to finish while holder was open")
						}
					} else {
						// Identify this schema's holder by backend PID rather than
						// accepting an unrelated lock wait elsewhere in the server.
						require.Eventually(t, func() bool {
							var waiting int64
							queryErr := mgr.Database.WithContext(writerCtx).Raw(`SELECT count(*) FROM pg_locks w JOIN pg_locks h
								ON w.locktype = h.locktype AND w.database = h.database
								AND w.classid = h.classid AND w.objid = h.objid AND w.objsubid = h.objsubid
								WHERE h.pid = ? AND h.locktype = 'advisory' AND h.granted AND NOT w.granted`, pid).Scan(&waiting).Error
							return queryErr == nil && waiting > 0
						}, 5*time.Second, 10*time.Millisecond, "second writer must wait on holder's family lock")
					}
					unblock()
					select {
					case err := <-holderDone:
						require.NoError(t, err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if !bypass {
						select {
						case err := <-done:
							require.NoError(t, err)
						case <-writerCtx.Done():
							t.Fatal("writer failed to finish after holder committed")
						}
					}
					var attrs []EntityAttribute
					require.NoError(t, api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ? AND scope = ?", "device", device.ID, "SHARED").Find(&attrs).Error)
					values := map[string]string{}
					for _, a := range attrs {
						values[a.AttrKey] = a.Value.String
					}
					want := values["a"] == "x" && values["b"] == "y"
					require.True(t, want, "both writes must have committed")
					got := isMemberOf(t, api, ctx, "device", device.ID, g.ID, v.Version)
					if got != want {
						diverged++
					}
					if !bypass {
						require.Equal(t, want, got, "round %d: membership differs from committed selector", round)
					}
					require.NoError(t, api.DeregisterGroupVersionForScoping(ctx, g.Token, v.Version))
				}()
			}
			if bypass {
				require.Positive(t, diverged, "negative control must expose stale membership")
			}
		})
	}
}
