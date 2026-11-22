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
		if err := mark(tx, f.tenant); err != nil {
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
		purgeMark := func(tx *gorm.DB) error { return purgePlan.BeforeDelete(tx, f.tenant) }
		for _, mark := range []func(*gorm.DB) error{purgeMark, markAttributeOrphanSweep} {
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

// M1: a pass runs on ONE replica. While another replica holds the sweep's session lock, a pass
// removes nothing and returns cleanly; once the lock is free, the next pass removes the orphans.
func TestAttributeOrphanSweepRunsOnOneReplicaPostgres(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	protocolSet(t, f, "SERVER", "orphan-gate")
	require.NoError(t, f.api.RDB.DB(f.ctx).Exec("DELETE FROM devices WHERE id = ?", f.device.ID).Error)
	count := func() int64 {
		n, err := f.api.CountOrphanedDeviceAttributes(f.ctx)
		require.NoError(t, err)
		return n
	}
	require.Positive(t, count())
	require.NoError(t, f.api.RDB.DB(f.ctx).Connection(func(other *gorm.DB) error {
		var held bool
		require.NoError(t, other.Raw("SELECT pg_try_advisory_lock(?)", attributeOrphanSweepLockKey).Scan(&held).Error)
		require.True(t, held)
		require.NoError(t, f.api.SweepAllDeviceAttributeOrphans(f.ctx))
		require.Positive(t, count(), "a replica that did not take the sweep lock must not sweep")
		return other.Exec("SELECT pg_advisory_unlock(?)", attributeOrphanSweepLockKey).Error
	}))
	require.NoError(t, f.api.SweepAllDeviceAttributeOrphans(f.ctx))
	require.Zero(t, count())
}

// #2978: tenant purge locks the tenant's device rows before it deletes any child row. A report
// (or any writer) that holds its device row KEY SHARE and then touches a configuration row must
// never meet a purge that has already deleted that row and is waiting for the device: the purge
// is observed waiting on the device row while the configuration row is still free.
func TestTenantPurgeTakesDeviceRowsFirstPostgres(t *testing.T) {
	f := newAttributeProtocolFixture(t)
	db := f.api.RDB.Database
	// A device configuration state row exists for the device, written the way a report writes it.
	require.NoError(t, f.api.RDB.DB(f.ctx).Exec(`INSERT INTO device_configuration_states (tenant_id, device_id, created_at, updated_at)
		VALUES (?, ?, now(), now()) ON CONFLICT DO NOTHING`, f.tenant, f.device.ID).Error)

	holder := db.Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, holder.Exec(`SELECT id FROM devices WHERE id = ? FOR KEY SHARE`, f.device.ID).Error)

	sys := core.WithSystemContext(context.Background())
	plan, err := tenantpurge.Classify(sys, db)
	require.NoError(t, err)
	attrprotocol.RegisterPurge(plan)
	purged := make(chan error, 1)
	go func() {
		_, err := tenantpurge.Sweep(sys, db, plan, f.tenant, time.Now(), nil)
		purged <- err
	}()
	// The purge waits on the device row the holder shares ...
	require.Eventually(t, func() bool {
		var waiting int64
		_ = db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%FROM "device-management".devices%FOR UPDATE%'`).Scan(&waiting).Error
		return waiting > 0
	}, 10*time.Second, 20*time.Millisecond, "purge must wait on the tenant's device rows first")
	// ... while it holds no child row: the holder can still update its configuration row.
	require.NoError(t, holder.Exec(`UPDATE device_configuration_states SET updated_at = now() WHERE device_id = ?`, f.device.ID).Error)
	require.NoError(t, holder.Commit().Error)
	require.NoError(t, <-purged, "the purge completes once the writer commits; no 40P01")
}
