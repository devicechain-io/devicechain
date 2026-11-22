// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/attrprotocol"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type attributeProtocolFixture struct {
	api    *Api
	ctx    context.Context
	device *Device
	tenant string
}

func newAttributeProtocolFixture(t *testing.T) attributeProtocolFixture {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("protocol%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), time.Minute)
	t.Cleanup(cancel)
	device := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	f := attributeProtocolFixture{api, ctx, device, tenant}
	installAttributeRejectingTrigger(t, f)
	return f
}

// These are TEST-ONLY triggers. WHEN limits every event to this fixture's tenant,
// including both images of UPDATE. Other packages share this database.
func installAttributeRejectingTrigger(t *testing.T, f attributeProtocolFixture) {
	t.Helper()
	db := f.api.RDB.Database
	name := fmt.Sprintf("dcap_%d", f.device.ID)
	fn := fmt.Sprintf(`"device-management".%s_guard`, name)
	t.Cleanup(func() {
		for _, event := range []string{"insert", "update", "delete"} {
			if err := db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s_%s ON "device-management".entity_attributes`, name, event)).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Exec("DROP FUNCTION IF EXISTS " + fn + "()").Error; err != nil {
			t.Error(err)
		}
	})
	require.NoError(t, db.Exec(`CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $dcap$
BEGIN
  IF current_setting('devicechain.attr_protocol', true) IS DISTINCT FROM '1' THEN
    RAISE EXCEPTION 'unmarked device attribute write' USING ERRCODE = 'DCAP1';
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$dcap$`).Error)
	own := func(image string) string {
		// tenant is generated above from a timestamp, never caller input.
		return fmt.Sprintf("(%s.tenant_id = '%s' AND %s.entity_type = 'device')", image, f.tenant, image)
	}
	for _, event := range []struct{ name, when string }{
		{"insert", own("NEW")},
		{"update", own("NEW") + " OR " + own("OLD")},
		{"delete", own("OLD")},
	} {
		require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s_%s BEFORE %s ON "device-management".entity_attributes
FOR EACH ROW WHEN (%s) EXECUTE FUNCTION %s()`, name, event.name, event.name, event.when, fn)).Error)
	}
}

func protocolSet(t *testing.T, f attributeProtocolFixture, scope, value string) *EntityAttribute {
	t.Helper()
	row, err := f.api.SetEntityAttribute(f.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: f.device.Token, Scope: scope, AttrKey: "k", ValueType: "STRING", Value: &value})
	require.NoError(t, err)
	return row
}

func TestAttributeProtocolEveryEntrance(t *testing.T) {
	exercises := map[string]func(*testing.T, attributeProtocolFixture){
		"setEntityAttribute": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SERVER", "set")
		},
		"deleteEntityAttribute": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SERVER", "unset")
			_, err := f.api.DeleteEntityAttribute(f.ctx, "device", f.device.Token, "SERVER", "k")
			require.NoError(t, err)
		},
		"restore": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SHARED", "first")
			protocolSet(t, f, "SHARED", "second")
			_, err := f.api.RestoreDeviceConfiguration(f.ctx, f.device.Token, 1)
			require.NoError(t, err)
		},
		"copy": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SHARED", "copy")
			_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "target", DeviceTypeToken: "sensor"})
			require.NoError(t, err)
			_, err = f.api.CopyDeviceConfiguration(f.ctx, f.device.Token, "target", nil)
			require.NoError(t, err)
		},
		"facet import": func(t *testing.T, f attributeProtocolFixture) {
			declareFacet(t, f.api, f.ctx, "climate", "STRING")
			rows, err := f.api.ImportFacetValues(f.ctx, []*FacetValueImportRow{{MemberType: "device", Token: f.device.Token, Key: "climate", Value: "cold"}}, false)
			require.NoError(t, err)
			require.Equal(t, "SET", rows[0].Outcome)
		},
		"repair": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SERVER", "repair")
			pending := PendingAttributeRepair{EntityType: "device", EntityId: f.device.ID, Scope: "SERVER", AttrKey: "k", CreatedAt: time.Now()}
			require.NoError(t, f.api.RDB.DB(f.ctx).Create(&pending).Error)
			previous := attributeBeforeCommitHook
			attributeBeforeCommitHook = func(tx *gorm.DB) error {
				assertAttributeMarker(t, tx, "1")
				return nil
			}
			t.Cleanup(func() { attributeBeforeCommitHook = previous })
			require.NoError(t, f.api.repairAttribute(f.ctx, pending))
		},
		"device delete": func(t *testing.T, f attributeProtocolFixture) {
			protocolSet(t, f, "SERVER", "delete")
			deleted, err := f.api.DeleteDevice(f.ctx, f.device.Token)
			require.NoError(t, err)
			require.True(t, deleted)
			_, found, err := attributeGeneration(f.api.RDB.DB(f.ctx), f.device.ID, "SERVER", "k")
			require.NoError(t, err)
			require.False(t, found)
		},
		"tenant purge": testAttributePurgeMarker,
		"orphan sweep": func(t *testing.T, f attributeProtocolFixture) {
			testAttributeOrphanSweep(t, f.api, f.ctx, f.device)
		},
		"dedupe migration": func(t *testing.T, _ attributeProtocolFixture) {
			// Named historic exception: exercise the actual frozen migration in an
			// isolated SQLite database, before any enforcement. Replaying its global
			// dedupe on the shared Postgres database could touch another test's rows.
			api, ctx := configTestApi(t)
			require.NoError(t, api.RDB.Database.Exec("DROP INDEX uix_entity_attributes_key").Error)
			for range 2 {
				require.NoError(t, api.RDB.DB(ctx).Create(&EntityAttribute{EntityType: "device", EntityId: 1, Scope: "SERVER", AttrKey: "historic", ValueType: "STRING", LastUpdated: time.Now()}).Error)
			}
			require.NoError(t, schema.NewEntityAttributeUniqueKeySchema().Migrate(api.RDB.DB(core.WithSystemContext(ctx))))
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).Count(&n).Error)
			require.Equal(t, int64(1), n)
		},
	}
	exercised := map[string]bool{}
	for _, site := range attributeWriteSites {
		run, exists := exercises[site.Name]
		if !exists {
			continue // The completeness check below names the missing entrance.
		}
		if t.Run(site.Name, func(t *testing.T) {
			var f attributeProtocolFixture
			if site.Name != "dedupe migration" {
				f = newAttributeProtocolFixture(t)
			}
			run(t, f)
		}) {
			exercised[site.Name] = true
		}
	}
	require.NoError(t, checkAttributeWriteSiteCoverage(exercised))
}

func testAttributePurgeMarker(t *testing.T, f attributeProtocolFixture) {
	protocolSet(t, f, "SERVER", "purge")
	sys := core.WithSystemContext(f.ctx)
	plan, err := tenantpurge.Classify(sys, f.api.RDB.Database)
	require.NoError(t, err)
	attrprotocol.RegisterPurge(plan)
	mark := plan.BeforeDelete
	require.NotNil(t, mark)
	plan.BeforeDelete = nil
	_, err = tenantpurge.Sweep(sys, f.api.RDB.Database, plan, f.tenant, time.Now(), nil)
	require.Equal(t, "DCAP1", attributeSQLState(err), "the hook-free counterweight must reject")
	err = f.api.RDB.DB(sys).Transaction(func(tx *gorm.DB) error {
		if err := mark(tx); err != nil {
			return err
		}
		assertAttributeMarker(t, tx, "1")
		var held int64
		require.NoError(t, tx.Raw("SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted").Scan(&held).Error)
		require.Zero(t, held, "purge must not acquire per-device advisory locks")
		return nil
	})
	require.NoError(t, err)
	// The registered plan, run through the real sweep, passes the rejecting test trigger. (The
	// purge executor's own registration is pinned in user-management's purge package.)
	plan.BeforeDelete = mark
	_, err = tenantpurge.Sweep(sys, f.api.RDB.Database, plan, f.tenant, time.Now(), nil)
	require.NoError(t, err)
	for _, table := range []string{"devices", "entity_attributes", "entity_attribute_generations"} {
		var n int64
		require.NoError(t, f.api.RDB.Database.Raw("SELECT count(*) FROM "+table+" WHERE tenant_id=?", f.tenant).Scan(&n).Error)
		require.Zero(t, n, table)
	}
}

func TestAttributeProtocolRejectsRawWrites(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	row := protocolSet(t, f, "SERVER", "before")
	db := f.api.RDB.DB(f.ctx)
	err := db.Exec(`INSERT INTO entity_attributes (tenant_id, entity_type, entity_id, scope, attr_key, value_type, last_updated)
VALUES (?, 'device', ?, 'SERVER', 'raw-insert', 'STRING', CURRENT_TIMESTAMP)`, f.tenant, f.device.ID).Error
	require.Equal(t, "DCAP1", attributeSQLState(err))
	err = db.Exec("UPDATE entity_attributes SET value='raw-update' WHERE tenant_id=? AND id=?", f.tenant, row.ID).Error
	require.Equal(t, "DCAP1", attributeSQLState(err))
	err = db.Unscoped().Delete(row).Error
	require.Equal(t, "DCAP1", attributeSQLState(err))
}

func assertAttributeMarker(t *testing.T, db *gorm.DB, want string) {
	t.Helper()
	var got string
	require.NoError(t, db.Raw("SELECT coalesce(current_setting('devicechain.attr_protocol', true), '')").Scan(&got).Error)
	require.Equal(t, want, got)
}

func TestAttributeProtocolMarkerLifetimeSameBackend(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	require.NoError(t, f.api.RDB.DB(f.ctx).Connection(func(conn *gorm.DB) error {
		var pid, after int
		require.NoError(t, conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
		defer func() { require.NoError(t, conn.Exec("RESET devicechain.attr_protocol").Error) }()
		assertAttributeMarker(t, conn, "")
		for _, commit := range []bool{true, false} {
			tx := conn.Begin()
			require.NoError(t, tx.Error)
			tx = asRunnerTx(tx)
			defer tx.Rollback()
			require.NoError(t, lockDeviceAttributes(f.ctx, tx, f.device.ID))
			assertAttributeMarker(t, tx, "1")
			if commit {
				require.NoError(t, tx.Commit().Error)
			} else {
				require.NoError(t, tx.Rollback().Error)
			}
			assertAttributeMarker(t, conn, "")
		}
		// A cancelled transaction context is not exercised on this pinned backend: cancelling an
		// in-flight transaction makes database/sql discard the connection ("bad connection"), so
		// the same-backend assertion below could not run. A cancelled transaction is rolled back
		// by the server, which the rollback case above already covers for the marker.

		tx := conn.Begin()
		require.NoError(t, tx.Error)
		tx = asRunnerTx(tx)
		defer tx.Rollback()
		require.NoError(t, lockDeviceAttributes(f.ctx, tx, f.device.ID))
		require.NoError(t, tx.SavePoint("after_marker").Error)
		require.NoError(t, lockDeviceAttributes(f.ctx, tx, f.device.ID))
		require.NoError(t, tx.RollbackTo("after_marker").Error)
		assertAttributeMarker(t, tx, "1") // Set before this savepoint: it survives.
		require.NoError(t, tx.Commit().Error)
		assertAttributeMarker(t, conn, "")

		tx = conn.Begin()
		require.NoError(t, tx.Error)
		tx = asRunnerTx(tx)
		defer tx.Rollback()
		require.NoError(t, tx.SavePoint("before_marker").Error)
		require.NoError(t, lockDeviceAttributes(f.ctx, tx, f.device.ID))
		assertAttributeMarker(t, tx, "1")
		require.NoError(t, tx.RollbackTo("before_marker").Error)
		assertAttributeMarker(t, tx, "") // SET LOCAL inside the savepoint is undone.
		value := "after-savepoint"
		_, err := f.api.applyAttributePatchTx(f.ctx, tx.Session(&gorm.Session{NewDB: true}), "device", f.device.ID, AttributePatch{{Scope: "SERVER", Key: "savepoint", ValueType: "STRING", Value: &value, LastUpdated: time.Now()}})
		require.NoError(t, err, "the owner must reestablish the marker, not trust cached Go state")
		assertAttributeMarker(t, tx, "1")
		require.NoError(t, tx.Rollback().Error)
		assertAttributeMarker(t, conn, "")
		// The marker-only destructive exceptions must be transaction-local too.
		purgePlan := &tenantpurge.Plan{Entries: []tenantpurge.Entry{{Table: tenantpurge.Table{Schema: "device-management", Name: "entity_attributes"}}}}
		attrprotocol.RegisterPurge(purgePlan)
		require.NotNil(t, purgePlan.BeforeDelete)
		for _, mark := range []func(*gorm.DB) error{purgePlan.BeforeDelete, markAttributeOrphanSweep} {
			for _, commit := range []bool{true, false} {
				tx = conn.Begin()
				require.NoError(t, tx.Error)
				defer tx.Rollback()
				require.NoError(t, mark(tx))
				assertAttributeMarker(t, tx, "1")
				if commit {
					require.NoError(t, tx.Commit().Error)
				} else {
					require.NoError(t, tx.Rollback().Error)
				}
				assertAttributeMarker(t, conn, "")
			}
		}
		require.NoError(t, conn.Raw("SELECT pg_backend_pid()").Scan(&after).Error)
		require.Equal(t, pid, after)
		return nil
	}))
}

func TestAttributeGenerationProgressionPostgres(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	testAttributeGenerationProgression(t, f.api, f.ctx, f.device.ID)
}

func TestAttributeOrphanSweepPostgres(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	testAttributeOrphanSweep(t, f.api, f.ctx, f.device)
}

func TestAttributeGenerationsMigrationReplayPostgres(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	db := f.api.RDB.DB(core.WithSystemContext(f.ctx))
	for range 2 {
		require.NoError(t, schema.NewAttributeGenerationsSchema().Migrate(db))
	}
}
