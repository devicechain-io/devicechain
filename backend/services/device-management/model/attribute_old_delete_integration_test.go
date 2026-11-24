// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

// T-del-2: the v1.8 device delete against a v1.9 attribute writer, in the order v1.8 runs it.
//
// During a v1.8 to v1.9 roll an old pod can delete a device while a new pod writes one of its
// attributes. The old delete removes the device's attribute rows BEFORE it locks the device row
// (v1.8.0 api_delete.go, deleteEdgeEntity: relationships, then `DELETE FROM entity_attributes
// WHERE entity_type = ? AND entity_id = ?`, then the cascade and the device row), whereas the
// new delete takes the device lock and the row first. So the new delete's guarantee does not
// hold for an old deleter, and the stated guarantee for the roll window is weaker: the two are
// either serialized, or one of them is a deadlock victim (40P01) and is retried or refused, or an
// ORPHAN attribute row is left that the sweep removes. Never a silent wrong result.
//
// The old delete is replayed as the SQL it issues, in its order, on a raw transaction: the old
// code is not in this tree, and a test that called the new DeleteDevice would be testing the
// protocol the old pod does not follow. The control at the end does exactly that, to show the
// orphan below comes from the OLD order and not from the fixture.
func oldDeleteSteps(tx *gorm.DB, tenant string, id uint) (step1, step2 func() error) {
	step1 = func() error {
		// v1.8.0 deleteEdgeEntity: the owned attributes, with no device lock taken first.
		return tx.Exec("DELETE FROM entity_attributes WHERE tenant_id = ? AND entity_type = 'device' AND entity_id = ?", tenant, id).Error
	}
	step2 = func() error {
		// then the device row itself, which waits for any writer's FOR KEY SHARE on it.
		return tx.Exec("DELETE FROM devices WHERE tenant_id = ? AND id = ?", tenant, id).Error
	}
	return step1, step2
}

func orphanCount(t *testing.T, api *Api, ctx context.Context) int64 {
	t.Helper()
	n, err := api.CountOrphanedDeviceAttributes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Schedule A: the writer holds the device (its lock and KEY SHARE) and has not written yet. The
// old delete removes the attribute rows it can see (none), then waits for the device row, and when the
// writer commits the delete goes through: the writer's row outlives its device. That is the orphan
// the roll window states, and the sweep removes it.
func TestAttributeOldDeleteT2WriterHoldsDeviceThenDeleteLeavesAnOrphan(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	tenant := dev.TenantId
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
	writeDone := attributeAsyncWrite(api, ctx, dev.ID, nil, &receipt)
	attributeAwait(t, entered)

	old := attributeRawTx(t, api, ctx)
	step1, step2 := oldDeleteSteps(old, tenant, dev.ID)
	if err := step1(); err != nil {
		t.Fatalf("the old delete's attribute step: %v", err)
	}
	pid := make(chan int, 1)
	deleteDone := make(chan error, 1)
	go func() {
		var p int
		if err := old.Raw("SELECT pg_backend_pid()").Scan(&p).Error; err != nil {
			deleteDone <- err
			return
		}
		pid <- p
		deleteDone <- step2()
	}()
	attributeWaitPID(t, api.RDB.DB(ctx), attributeAwait(t, pid), "transactionid")

	close(release)
	if err := attributeAwait(t, writeDone); err != nil {
		t.Fatalf("the new writer must succeed: it held the device first: %v", err)
	}
	if err := attributeAwait(t, deleteDone); err != nil {
		t.Fatalf("the old delete: %v", err)
	}
	if err := old.Commit().Error; err != nil {
		t.Fatal(err)
	}

	// The residual, observed: the device is gone and the writer's committed row is not.
	if n := orphanCount(t, api, ctx); n == 0 {
		t.Fatal("no orphan after an old delete beat a writer's commit: the stated residual did not occur, so this test is not exercising the old delete order")
	}
	if err := api.SweepAllDeviceAttributeOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	if n := orphanCount(t, api, ctx); n != 0 {
		t.Fatalf("%d orphan(s) survived the sweep", n)
	}
}

// Schedule B: the attribute row already exists and the writer holds the device, so the old delete's
// attribute step and the writer both want the same row while the delete's next step wants the
// device the writer holds: a deadlock. One side is the victim (40P01). If it is the writer, the
// runner retries it and, the device being gone, it is REFUSED, not applied; if it is the delete, nothing
// is deleted and the writer commits. Either way nothing is left inconsistent after the sweep.
func TestAttributeOldDeleteT2DeadlockIsResolvedNeverSilent(t *testing.T) {
	api, ctx, dev := attributeLockFixture(t)
	tenant := dev.TenantId
	// the existing row
	if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		_, err := api.applyAttributePatchTx(ctx, tx, "device", dev.ID, attributeSetPatch("V1"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
	writeDone := attributeAsyncWrite(api, ctx, dev.ID, nil, &receipt)
	attributeAwait(t, entered)

	old := attributeRawTx(t, api, ctx)
	// "Old" means a v1.9 pod: it marks its writes and only takes its locks in the old order. A pod
	// from before the marker is refused by the database instead (attribute_writer_protocol_integration_test).
	markAsProtocolWriter(t, old)
	step1, step2 := oldDeleteSteps(old, tenant, dev.ID)
	deleteDone := make(chan error, 1)
	go func() {
		// the attribute step may wait on the writer's row; the device step waits on its KEY SHARE
		if err := step1(); err != nil {
			deleteDone <- err
			return
		}
		deleteDone <- step2()
	}()
	// let the old delete reach its wait, then let the writer go on
	attributePoll(t, func() (bool, error) {
		var n int64
		err := api.RDB.DB(ctx).Raw("SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype IN ('transactionid','tuple')").Scan(&n).Error
		return n > 0, err
	})
	close(release)
	writeErr := attributeAwait(t, writeDone)
	deleteErr := attributeAwait(t, deleteDone)
	if deleteErr == nil {
		if err := old.Commit().Error; err != nil {
			deleteErr = err
		}
	} else {
		_ = old.Rollback().Error
	}

	switch {
	case writeErr == nil && deleteErr == nil:
		// serialized: the writer committed, then the delete removed the device (and may have left the
		// writer's row, which the sweep removes below)
	case writeErr == nil && deleteErr != nil:
		if attributeSQLState(deleteErr) != "40P01" {
			t.Fatalf("the old delete failed with %v, not a deadlock", deleteErr)
		}
		var n int64
		if err := api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", dev.ID).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("the delete lost the deadlock but the device is gone (count %d, err %v)", n, err)
		}
	case writeErr != nil && deleteErr == nil:
		if !errors.Is(writeErr, gorm.ErrRecordNotFound) && attributeSQLState(writeErr) != "40P01" {
			t.Fatalf("the writer failed with %v: after a lost deadlock it must be refused as not found, or give up on 40P01", writeErr)
		}
	default:
		t.Fatalf("both sides failed: writer %v, delete %v", writeErr, deleteErr)
	}
	if err := api.SweepAllDeviceAttributeOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	if n := orphanCount(t, api, ctx); n != 0 {
		t.Fatalf("%d orphan(s) survived the sweep", n)
	}
}

// THE NEGATIVE CONTROL: the same writer-holds-device schedule, but the delete is the NEW one, which
// takes the device lock first. It waits for the writer, and nothing is orphaned. So the orphan in
// schedule A is the old delete order's, and the sweep assertion above measures something.
func TestAttributeOldDeleteT2ControlNewDeleteLeavesNoOrphan(t *testing.T) {
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
	writeDone := attributeAsyncWrite(api, ctx, dev.ID, nil, &receipt)
	attributeAwait(t, entered)
	deleteDone := make(chan error, 1)
	go func() { _, err := api.DeleteDevice(ctx, "dev"); deleteDone <- err }()
	attributeWaitDeviceAdvisory(t, api.RDB.DB(ctx), dev.ID)
	close(release)
	if err := attributeAwait(t, writeDone); err != nil {
		t.Fatal(err)
	}
	if err := attributeAwait(t, deleteDone); err != nil {
		t.Fatal(err)
	}
	if n := orphanCount(t, api, ctx); n != 0 {
		t.Fatalf("the NEW delete left %d orphan(s) behind a writer; the guarantee it gives did not hold", n)
	}
}
