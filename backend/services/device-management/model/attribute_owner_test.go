// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"
)

func TestAttributeOwnerSQLiteDialectAndReceipt(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	var statements []string
	capture := func(db *gorm.DB) { statements = append(statements, db.Statement.SQL.String()) }
	for _, c := range []struct {
		name     string
		register func(string, func(*gorm.DB)) error
	}{
		{"query", api.RDB.Database.Callback().Query().After("gorm:query").Register},
		{"raw", api.RDB.Database.Callback().Raw().After("gorm:raw").Register},
	} {
		if err := c.register("attribute_sql_capture_"+c.name, capture); err != nil {
			t.Fatal(err)
		}
	}
	value := "first"
	var receipt *AttributeReceipt
	err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		var err error
		receipt, err = api.applyAttributePatchTx(ctx, tx, "device", dev.ID, AttributePatch{
			{Scope: "SERVER", Key: "z", ValueType: "STRING", Value: &value, LastUpdated: time.Now()},
			{Scope: "CLIENT", Key: "a", ValueType: "STRING", Value: &value, LastUpdated: time.Now()},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Duplicates || receipt.RowsAffected != 2 || len(receipt.Changes) != 2 || receipt.Changes[0].Key != "a" || len(receipt.Changes[0].Before) != 0 || receipt.Changes[0].After == nil {
		t.Fatalf("bad receipt: %+v", receipt)
	}
	err = api.runDeviceAttributeTx(ctx, "unset", func(tx *gorm.DB) error {
		var err error
		receipt, err = api.applyAttributePatchTx(ctx, tx, "device", dev.ID, AttributePatch{{Scope: "CLIENT", Key: "a", Unset: true}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RowsAffected != 1 || len(receipt.Changes[0].Before) != 1 || receipt.Changes[0].After != nil {
		t.Fatalf("bad unset receipt: %+v", receipt)
	}
	for _, sql := range statements {
		lower := strings.ToLower(sql)
		for _, forbidden := range []string{"dc_entity_attr_lock", "for key share", "nowait"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("SQLite issued lock SQL: %s", sql)
			}
		}
	}
	beforeMigrationStatements := len(statements)
	if err := schema.NewDeviceAttributeLockSchema().Migrate(api.RDB.Database); err != nil {
		t.Fatal(err)
	}
	if len(statements) != beforeMigrationStatements {
		t.Fatal("SQLite migration issued SQL")
	}
	if err := schema.NewDeviceAttributeLockSchema().Rollback(api.RDB.Database); err == nil {
		t.Fatal("rollback must refuse")
	}
}
func TestAttributeOwnerReportsDuplicates(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	if err := api.RDB.Database.Exec("DROP INDEX uix_entity_attributes_key").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := api.RDB.DB(ctx).Create(&EntityAttribute{EntityType: "device", EntityId: dev.ID, Scope: "CLIENT", AttrKey: "k", ValueType: "STRING"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	err := api.runDeviceAttributeTx(ctx, "unset", func(tx *gorm.DB) error {
		receipt, err := api.applyAttributePatchTx(ctx, tx, "device", dev.ID, AttributePatch{{Scope: "CLIENT", Key: "k", Unset: true}})
		if err == nil && (!receipt.Duplicates || len(receipt.Changes[0].Before) != 2 || receipt.RowsAffected != 2) {
			t.Fatalf("duplicates hidden: %+v", receipt)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestAttributeTransactionRetriesRollbackAndBounds(t *testing.T) {
	api, ctx := configTestApi(t)
	for _, tc := range []struct {
		name, state string
		fail, want  int
		busy        bool
	}{
		{"yield-success", "55P03", 2, 3, false}, {"yield-exhaustion", "55P03", 99, 5, true},
		{"deadlock-success", "40P01", 2, 3, false}, {"deadlock-exhaustion", "40P01", 99, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
				calls++
				if err := tx.Create(&EntityAttribute{EntityType: "asset", EntityId: 1, Scope: "CLIENT", AttrKey: tc.name, ValueType: "STRING"}).Error; err != nil {
					return err
				}
				if calls <= tc.fail {
					if tc.state == "55P03" {
						return errAttributeRowBusy
					}
					return &pgconn.PgError{Code: tc.state}
				}
				return nil
			})
			if calls != tc.want {
				t.Fatalf("attempts=%d want %d: %v", calls, tc.want, err)
			}
			var busy *AttributeWriteBusyError
			if errors.As(err, &busy) != tc.busy {
				t.Fatalf("busy classification: %v", err)
			}
			if busy != nil && (busy.Extensions()["code"] != "ATTRIBUTE_WRITE_BUSY" || !busy.Retryable()) {
				t.Fatal("busy not retryable on wire")
			}
			var n int64
			if e := api.RDB.DB(ctx).Model(&EntityAttribute{}).Where("attr_key = ?", tc.name).Count(&n).Error; e != nil {
				t.Fatal(e)
			}
			wantRows := int64(0)
			if tc.fail < tc.want {
				wantRows = 1
			}
			if n != wantRows {
				t.Fatalf("rolled-back rows survived: %d want %d", n, wantRows)
			}
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := api.runDeviceAttributeTx(cancelled, "set", func(*gorm.DB) error { return errAttributeRowBusy }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, path := range []string{"set", "unset", "restore", "copy", "delete", "import", "repair"} {
		if testutil.ToFloat64(deviceAttributeWriteDeadlocks.WithLabelValues(path)) < 0 {
			t.Fatal("missing metric")
		}
	}
}
func TestAttributeRetryEmitsOnlyAfterSuccessfulCommit(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, nil)
	publisher := &captureAttr{}
	api.DeviceAttributePublisher = publisher
	calls := 0
	if err := api.RDB.Database.Callback().Create().After("gorm:create").Register("retry_after_attribute_write", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Name == "EntityAttribute" {
			calls++
			if calls < 3 {
				db.AddError(&pgconn.PgError{Code: "40P01"})
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	value := "42"
	row, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "dev", Scope: "SERVER", AttrKey: "k", ValueType: "LONG", Value: &value})
	if err != nil {
		t.Fatal(err)
	}
	if row.ID == 0 || calls != 3 || len(publisher.events) != 1 {
		t.Fatalf("calls=%d row=%+v emits=%d", calls, row, len(publisher.events))
	}
	var n int64
	if err := api.RDB.DB(ctx).Model(&EntityAttribute{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rolled-back attempts left %d rows", n)
	}
}
