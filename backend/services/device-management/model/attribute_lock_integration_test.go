// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"
)

func attributeLockFixture(t *testing.T) (*Api, context.Context, *Device) {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("attr%d", time.Now().UnixNano())), 30*time.Second)
	t.Cleanup(cancel)
	return api, ctx, seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
}
func attributePoll(t *testing.T, condition func() (bool, error)) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := condition()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("lock condition never observed")
		case <-tick.C:
		}
	}
}
func attributeWaitPID(t *testing.T, db *gorm.DB, pid int, kind string) {
	t.Helper()
	attributePoll(t, func() (bool, error) {
		var n int64
		err := db.Raw("SELECT count(*) FROM pg_locks WHERE pid=? AND NOT granted AND locktype=?", pid, kind).Scan(&n).Error
		return n > 0, err
	})
}
func attributeRawTx(t *testing.T, api *Api, ctx context.Context) *gorm.DB {
	t.Helper()
	tx := api.RDB.DB(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}
func attributeSetPatch(value string) AttributePatch {
	return AttributePatch{{Scope: "SHARED", Key: "k", ValueType: "STRING", Value: &value, LastUpdated: time.Now()}}
}
func attributeAsyncWrite(api *Api, ctx context.Context, id uint, pid chan<- int, receipt **AttributeReceipt) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
			if pid != nil {
				var p int
				if err := tx.Raw("SELECT pg_backend_pid()").Scan(&p).Error; err != nil {
					return err
				}
				select {
				case pid <- p:
				default:
				}
			}
			var err error
			*receipt, err = api.applyAttributePatchTx(ctx, tx, "device", id, attributeSetPatch("V2"))
			return err
		})
	}()
	return done
}
func TestAttributeLockT1BeforeImageUnderLock(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	helper := attributeRawTx(t, api, ctx)
	if err := helper.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, dev.ID).Error; err != nil {
		t.Fatal(err)
	}
	markAsProtocolWriter(t, helper) // the helper plays a protocol writer holding the lock
	row := &EntityAttribute{EntityType: "device", EntityId: dev.ID, Scope: "SHARED", AttrKey: "k", ValueType: "STRING", LastUpdated: time.Now()}
	row.Value.Valid, row.Value.String = true, "V1"
	if err := upsertAttributeOnTx(helper, row); err != nil {
		t.Fatal(err)
	}
	pid := make(chan int, 10)
	var receipt *AttributeReceipt
	done := attributeAsyncWrite(api, ctx, dev.ID, pid, &receipt)
	attributeWaitPID(t, api.RDB.DB(ctx), attributeAwait(t, pid), "advisory")
	if err := helper.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Changes[0].Before) != 1 || receipt.Changes[0].Before[0].Value.String != "V1" {
		t.Fatalf("before image read outside lock: %+v", receipt)
	}
}
func TestAttributeLockT2YieldsAndBusy(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(fmt.Sprint(release), func(t *testing.T) {
			api, ctx, dev := attributeLockFixture(t)
			setConfigAttr(t, api, ctx, AttributeScopeShared, "k", AttributeValueString, "V0")
			helper := attributeRawTx(t, api, ctx)
			if err := helper.Exec("SELECT id FROM entity_attributes WHERE entity_type='device' AND entity_id=? AND attr_key='k' FOR UPDATE", dev.ID).Error; err != nil {
				t.Fatal(err)
			}
			// The release case must not race the retry budget: the first yield parks the writer
			// (rolled back, holding nothing) until the helper has committed, so the next attempt
			// is guaranteed to find the row free.
			yielded, resume := make(chan struct{}), make(chan struct{})
			if release {
				var once sync.Once
				attributeYieldHook = func(int) { once.Do(func() { close(yielded); <-resume }) }
				t.Cleanup(func() {
					attributeYieldHook = nil
					select {
					case <-resume:
					default:
						close(resume)
					}
				})
			}
			yields := testutil.ToFloat64(deviceAttributeWriteYields.WithLabelValues("set", "retried"))
			deadlocks := testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("set"))
			pid := make(chan int, 10)
			var receipt *AttributeReceipt
			done := attributeAsyncWrite(api, ctx, dev.ID, pid, &receipt)
			writerPID := attributeAwait(t, pid)
			attributePoll(t, func() (bool, error) {
				return testutil.ToFloat64(deviceAttributeWriteYields.WithLabelValues("set", "retried")) > yields, nil
			})
			// NOWAIT must never leave a writer holding lock 1 queued on a legacy row lock.
			var illegal int64
			if err := api.RDB.DB(ctx).Raw(`SELECT count(*) FROM pg_locks w WHERE w.pid=? AND NOT w.granted AND w.locktype IN ('tuple','transactionid') AND EXISTS (SELECT 1 FROM pg_locks h WHERE h.pid=w.pid AND h.granted AND h.locktype='advisory')`, writerPID).Scan(&illegal).Error; err != nil {
				t.Fatal(err)
			}
			if illegal != 0 {
				t.Fatal("writer waited for an attribute row while holding advisory lock")
			}
			if release {
				attributeAwait(t, yielded)
				if err := helper.Commit().Error; err != nil {
					t.Fatal(err)
				}
				close(resume)
			}
			err := attributeAwait(t, done)
			if release {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var busy *AttributeWriteBusyError
				if !errors.As(err, &busy) || busy.Extensions()["code"] != "ATTRIBUTE_WRITE_BUSY" {
					t.Fatalf("wanted busy, got %v", err)
				}
				var row EntityAttribute
				if err := api.RDB.DB(ctx).Where("entity_id=? AND attr_key='k'", dev.ID).First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.Value.String != "V0" {
					t.Fatal("busy write committed")
				}
				if err := helper.Commit().Error; err != nil {
					t.Fatal(err)
				}
			}
			if testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("set")) != deadlocks {
				t.Fatal("yield produced a deadlock")
			}
		})
	}
}
func TestAttributeLockT3DeleteSerialization(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	attributeAfterReadHook = func(*gorm.DB) error { close(entered); <-release; return nil }
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		attributeAfterReadHook = nil
	})
	var receipt *AttributeReceipt
	done := attributeAsyncWrite(api, ctx, dev.ID, nil, &receipt)
	attributeAwait(t, entered)
	deleteDone := make(chan error, 1)
	go func() { _, err := api.DeleteDevice(ctx, "dev"); deleteDone <- err }()
	attributePoll(t, func() (bool, error) {
		var n int64
		err := api.RDB.DB(ctx).Raw("SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype='advisory'").Scan(&n).Error
		return n > 0, err
	})
	close(release)
	if err := attributeAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, deleteDone); err != nil {
		t.Fatal(err)
	}
	attributeAfterReadHook = nil
	var n int64
	if err := api.RDB.DB(ctx).Unscoped().Model(&EntityAttribute{}).Where("entity_type='device' AND entity_id=?", dev.ID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("attributes survived delete")
	}
	err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		_, err := api.applyAttributePatchTx(ctx, tx, "device", dev.ID, attributeSetPatch("late"))
		return err
	})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("write after delete: %v", err)
	}
	if err := api.RDB.DB(ctx).Unscoped().Model(&EntityAttribute{}).Where("entity_id=?", dev.ID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("write resurrected attributes")
	}
}
func TestAttributeLockT3KeyShareBlocksHardDelete(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	helper := attributeRawTx(t, api, ctx)
	if err := helper.Exec("SELECT id FROM devices WHERE id=? FOR KEY SHARE", dev.ID).Error; err != nil {
		t.Fatal(err)
	}
	pid := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			var p int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&p).Error; err != nil {
				return err
			}
			pid <- p
			return tx.Exec("DELETE FROM devices WHERE id=?", dev.ID).Error
		})
	}()
	attributeWaitPID(t, api.RDB.DB(ctx), attributeAwait(t, pid), "transactionid")
	if err := helper.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, done); err != nil {
		t.Fatal(err)
	}
}
func TestAttributeLockT22MissingFunctionAndMigration(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	db := api.RDB.DB(ctx)
	migration := schema.NewDeviceAttributeLockSchema()
	var before, after string
	if err := db.Raw(`SELECT pg_get_functiondef('"device-management".dc_entity_attr_lock(text,bigint)'::regprocedure)`).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migration.Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Raw(`SELECT pg_get_functiondef('"device-management".dc_entity_attr_lock(text,bigint)'::regprocedure)`).Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	var functions int64
	if err := db.Raw(`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='device-management' AND p.proname='dc_entity_attr_lock'`).Scan(&functions).Error; err != nil {
		t.Fatal(err)
	}
	if functions != 1 {
		t.Fatalf("lock function exists %d times", functions)
	}
	if before != after {
		t.Fatal("migration changed function body")
	}
	// Transactional DDL keeps the dropped function invisible to other connections;
	// rollback restores it even if the expected write errors abort the transaction.
	tx := attributeRawTx(t, api, ctx)
	t.Cleanup(func() {
		_ = tx.Rollback().Error
		if err := migration.Migrate(db); err != nil {
			t.Error(err)
		}
	})
	if err := tx.Exec(`DROP FUNCTION "device-management".dc_entity_attr_lock(text,bigint)`).Error; err != nil {
		t.Fatal(err)
	}
	// Inside the runner's state, so the error can only be the missing function, not the refusal
	// of a lock taken outside runDeviceAttributeTx.
	_, err := api.applyAttributePatchTx(ctx, asRunnerTx(tx), "device", dev.ID, attributeSetPatch("bad"))
	if err == nil || !strings.Contains(err.Error(), "dc_entity_attr_lock") {
		t.Fatalf("a missing lock function must fail the write on the function itself: %v", err)
	}
}
func TestAttributeLockRestoreCopyAndOneDevice(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	setConfigAttr(t, api, ctx, AttributeScopeShared, "k", AttributeValueString, "first")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "k", AttributeValueString, "second")
	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []uint{dev.ID, other.ID}
	var commits atomic.Int64
	attributeBeforeCommitHook = func(tx *gorm.DB) error {
		var n int64
		err := tx.Raw(`SELECT count(*) FROM pg_locks l WHERE l.pid=pg_backend_pid() AND l.locktype='advisory' AND l.granted AND l.objsubid=1 AND EXISTS (SELECT 1 FROM unnest(?::bigint[]) AS d(id) WHERE l.classid::bigint=((hashtextextended('dc.attr|device|'||d.id::text,0)>>32)&4294967295) AND l.objid::bigint=(hashtextextended('dc.attr|device|'||d.id::text,0)&4294967295))`, fmt.Sprintf("{%d,%d}", ids[0], ids[1])).Scan(&n).Error
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("device locks held=%d, want one", n)
		}
		commits.Add(1)
		return nil
	}
	t.Cleanup(func() { attributeBeforeCommitHook = nil })
	setConfigAttr(t, api, ctx, AttributeScopeClient, "client", AttributeValueString, "value")
	if _, err := api.DeleteEntityAttribute(ctx, "device", "dev", "CLIENT", "client"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.RestoreDeviceConfiguration(ctx, "dev", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CopyDeviceConfiguration(ctx, "dev", "other", nil); err != nil {
		t.Fatal(err)
	}
	declareFacet(t, api, ctx, "climate", "STRING")
	rows, err := api.ImportFacetValues(ctx, []*FacetValueImportRow{{MemberType: "device", Token: "dev", Key: "climate", Value: "cold"}}, false)
	if err != nil || rows[0].Outcome != "SET" {
		t.Fatalf("import: %+v %v", rows, err)
	}
	if _, err := api.DeleteDevice(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if commits.Load() != 6 {
		t.Fatalf("commit hook missed writer path: %d", commits.Load())
	}
	attributeBeforeCommitHook = nil
	// Park restore after its single all-key before-image read, then observe a set
	// waiting on lock 1. Both complete in the forced serial order without 40P01.
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	attributeAfterReadHook = func(*gorm.DB) error {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		attributeAfterReadHook = nil
	})
	restoreDeadlocks := testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("restore"))
	setDeadlocks := testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("set"))
	restore := make(chan error, 1)
	go func() { _, err := api.RestoreDeviceConfiguration(ctx, "dev", 2); restore <- err }()
	attributeAwait(t, entered)
	done := make(chan error, 1)
	go func() {
		value := "V2"
		_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "dev", Scope: "SHARED", AttrKey: "k", ValueType: "STRING", Value: &value})
		done <- err
	}()
	attributeWaitDeviceAdvisory(t, api.RDB.DB(ctx), dev.ID)
	close(release)
	if err := attributeAwait(t, restore); err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("restore")) != restoreDeadlocks || testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues("set")) != setDeadlocks {
		t.Fatal("new writers deadlocked")
	}
	attributeAfterReadHook = nil
	var row EntityAttribute
	if err := api.RDB.DB(ctx).Where("entity_id=? AND attr_key='k' AND scope='SHARED'", dev.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Value.String != "V2" {
		t.Fatal("result differs from forced serial order")
	}
	configuration, err := api.DeviceConfigurationByToken(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Stale || configuration.Desired == nil || configuration.Desired.Document != `{"k":"V2"}` {
		t.Fatalf("serial configuration mismatch: %+v", configuration)
	}
}

// The advisory lock alone makes writer/delete tests pass even without row 2.
// A raw hard deleter is needed to prove the primitive itself retains KEY SHARE.
func TestAttributeLockT3PrimitiveKeyShareBlocksHardDelete(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	attributeAfterReadHook = func(*gorm.DB) error { close(entered); <-release; return nil }
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		attributeAfterReadHook = nil
	})
	var receipt *AttributeReceipt
	done := attributeAsyncWrite(api, ctx, dev.ID, nil, &receipt)
	attributeAwait(t, entered)
	pid := make(chan int, 1)
	deleted := make(chan error, 1)
	go func() {
		deleted <- api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			var p int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&p).Error; err != nil {
				return err
			}
			pid <- p
			return tx.Exec("DELETE FROM devices WHERE id=?", dev.ID).Error
		})
	}()
	attributeWaitPID(t, api.RDB.DB(ctx), attributeAwait(t, pid), "transactionid")
	close(release)
	if err := attributeAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, deleted); err != nil {
		t.Fatal(err)
	}
	attributeAfterReadHook = nil
}

// Failure deadlines bound every channel wait; ordering is established only by
// channel handshakes and catalog observations, never by elapsed time.
func attributeAwait[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case value := <-values:
		return value
	case <-timer.C:
		t.Fatal("attribute lock worker did not reach its handshake")
	}
	var zero T
	return zero
}

func attributeWaitDeviceAdvisory(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	attributePoll(t, func() (bool, error) {
		var n int64
		err := db.Raw(`SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype='advisory' AND objsubid=1 AND classid::bigint=((hashtextextended('dc.attr|device|'||?::bigint::text,0)>>32)&4294967295) AND objid::bigint=(hashtextextended('dc.attr|device|'||?::bigint::text,0)&4294967295)`, id, id).Scan(&n).Error
		return n > 0, err
	})
}

// asRunnerTx gives a hand-opened test transaction the state runDeviceAttributeTx carries, so a
// test that must hold or pin its own transaction can still call the lock helpers, which refuse
// any transaction the runner did not open.
func asRunnerTx(tx *gorm.DB) *gorm.DB {
	return tx.WithContext(context.WithValue(tx.Statement.Context, attributeTxContextKey{}, &attributeTxState{path: "set"}))
}
