// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
)

func testAttributeGenerationProgression(t *testing.T, api *Api, ctx context.Context, deviceID uint) {
	t.Helper()
	var previous int64
	value := "same"
	for _, op := range []AttributeOp{
		{Scope: "SERVER", Key: "generation", Unset: true},
		{Scope: "SERVER", Key: "generation", ValueType: "STRING", Value: &value},
		{Scope: "SERVER", Key: "generation", ValueType: "STRING", Value: &value},
		{Scope: "SERVER", Key: "generation", Unset: true},
		{Scope: "SERVER", Key: "generation", Unset: true},
		{Scope: "SERVER", Key: "generation", ValueType: "STRING", Value: &value},
		{Scope: "SERVER", Key: "generation", ValueType: "STRING", Value: &value, SkipUnchanged: true},
	} {
		op.LastUpdated = time.Now()
		if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
			receipt, err := api.applyAttributePatchTx(ctx, tx, "device", deviceID, AttributePatch{op})
			if err != nil {
				return err
			}
			head, found, err := attributeGeneration(tx, deviceID, op.Scope, op.Key)
			if err != nil {
				return err
			}
			if !found || (!op.SkipUnchanged && head <= previous) || (op.SkipUnchanged && head != previous) {
				t.Fatalf("generation %d -> %d, found=%v, op=%+v", previous, head, found, op)
			}
			if !op.SkipUnchanged && receipt.Changes[0].Generation != head {
				t.Fatalf("receipt generation=%d, head=%d", receipt.Changes[0].Generation, head)
			}
			previous = head
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.runDeviceAttributeTx(ctx, "unset", func(tx *gorm.DB) error {
		_, err := api.applyAttributePatchTx(ctx, tx, "device", deviceID, AttributePatch{{Scope: "SERVER", Key: "never-written", Unset: true, SkipUnchanged: true}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if head, found, err := attributeGeneration(api.RDB.DB(ctx), deviceID, "SERVER", "never-written"); err != nil || found || head != 0 {
		t.Fatalf("NONE = (%d,%v,%v)", head, found, err)
	}
}

func TestAttributeGenerationProgressionSQLite(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	testAttributeGenerationProgression(t, api, ctx, dev.ID)
}

func TestAttributeProtocolRefusesRootHandle(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	if err := lockDeviceAttributes(ctx, api.RDB.DB(ctx), dev.ID); err == nil {
		t.Fatal("root handle accepted by device attribute protocol")
	}
	if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		return lockDeviceAttributes(ctx, tx, dev.ID)
	}); err != nil {
		t.Fatalf("transaction handle refused: %v", err)
	}
}

func TestAttributeProtocolRefusesSecondDevice(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "second", DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatal(err)
	}
	if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		if err := lockAttributeOwnerTx(ctx, tx, "device", dev.ID); err != nil {
			return err
		}
		if err := lockAttributeOwnerTx(ctx, tx, "device", other.ID); err == nil {
			t.Fatal("second device accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDestructivePurgeMarkerGuard(t *testing.T) {
	for _, source := range []string{
		"package model\nfunc scratch(tx *gorm.DB) { attrprotocol.Mark(tx) }",
		"package model\nfunc scratch(p *tenantpurge.Plan) { attrprotocol.RegisterPurge(p) }",
		"package model\nfunc scratch(tx *gorm.DB) { tx.Exec(attrprotocol.MarkerSQL) }",
	} {
		for _, name := range []string{"scratch.go", "api_attribute_owner.go", "api_attribute_orphans.go"} {
			violations, err := scanAttributeWriters(name, []byte(source))
			if err != nil || len(violations) == 0 {
				t.Fatalf("a marker use escaped the guard in %s: %s (%v)", name, source, err)
			}
		}
	}
}

func TestAttributeGenerationFollowsTransactionAndDevice(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	value := "value"
	patch := AttributePatch{{Scope: "SERVER", Key: "atomic", ValueType: "STRING", Value: &value, LastUpdated: time.Now()}}
	err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		if _, err := api.applyAttributePatchTx(ctx, tx, "device", dev.ID, patch); err != nil {
			return err
		}
		return context.Canceled
	})
	if err != context.Canceled {
		t.Fatalf("rollback result: %v", err)
	}
	if _, found, err := attributeGeneration(api.RDB.DB(ctx), dev.ID, "SERVER", "atomic"); err != nil || found {
		t.Fatalf("generation survived rollback: %v, %v", found, err)
	}
	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{dev.ID, other.ID} {
		if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
			_, err := api.applyAttributePatchTx(ctx, tx, "device", id, patch)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if deleted, err := api.DeleteDevice(ctx, dev.Token); err != nil || !deleted {
		t.Fatalf("delete: %v, %v", deleted, err)
	}
	if _, found, err := attributeGeneration(api.RDB.DB(ctx), dev.ID, "SERVER", "atomic"); err != nil || found {
		t.Fatalf("generation survived device deletion: %v, %v", found, err)
	}
	if _, found, err := attributeGeneration(api.RDB.DB(ctx), other.ID, "SERVER", "atomic"); err != nil || !found {
		t.Fatalf("another device's generation was deleted: %v, %v", found, err)
	}
}

func TestAttributeProtocolDirectLockRefusesSecondDevice(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	if err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		if err := lockDeviceAttributes(ctx, tx, dev.ID); err != nil {
			return err
		}
		if err := lockDeviceAttributes(ctx, tx, dev.ID+1); err == nil {
			t.Fatal("direct lock helper accepted a second device")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Outside runDeviceAttributeTx the lock is refused: on a root handle pg_advisory_xact_lock
// would release at once, and inside a plain Transaction there is no one-device state, no
// bounded retry and no post-commit discipline.
func TestAttributeLockRefusedOutsideTheRunner(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	if err := lockDeviceAttributes(ctx, api.RDB.DB(ctx), dev.ID); err == nil {
		t.Fatal("the lock helper accepted a root handle")
	}
	if err := lockAttributeOwnerTx(ctx, api.RDB.DB(ctx), "device", dev.ID); err == nil {
		t.Fatal("the owner lock accepted a root handle")
	}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceAttributes(ctx, tx, dev.ID); err == nil {
			t.Error("the lock helper accepted a plain transaction")
		}
		if err := lockAttributeOwnerTx(ctx, tx, "device", dev.ID); err == nil {
			t.Error("the owner lock accepted a plain transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
