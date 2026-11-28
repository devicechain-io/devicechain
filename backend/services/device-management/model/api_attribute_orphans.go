// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-microservice/attrprotocol"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
)

const attributeOrphanBatchSize = 1000

var deviceAttributeOrphansRemoved = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "device_attribute_orphans_removed_total",
	Help: "Orphaned device attribute and generation rows removed after previous-release device deletes.",
}, []string{"table"})

func init() {
	for _, table := range []string{"entity_attributes", "entity_attribute_generations"} {
		deviceAttributeOrphansRemoved.WithLabelValues(table)
	}
}

// CountOrphanedDeviceAttributes is the tenant-scoped readiness probe: device attribute rows
// (soft-deleted ones included, since they remain physical rows until this cleanup deletes them)
// and generation heads whose device no longer exists.
func (api *Api) CountOrphanedDeviceAttributes(ctx context.Context) (int64, error) {
	var total int64
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The count is a diagnostic over rows that can be many: bound it rather than let one
		// request hold a scan open.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SET LOCAL statement_timeout = '5s'").Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Unscoped().Model(&EntityAttribute{}).
			Where("entity_type = ?", "device").
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attributes.entity_id)").
			Count(&count).Error; err != nil {
			return err
		}
		var heads int64
		if err := tx.Model(&AttributeGeneration{}).
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attribute_generations.device_id)").
			Count(&heads).Error; err != nil {
			return err
		}
		total = count + heads
		return nil
	})
	return total, err
}

// fleetOrphanProbeTimeout bounds the activation gate's probe. A variable only for a test.
var fleetOrphanProbeTimeout = "5s"

// HasOrphanedDeviceAttributes is the activation gate's question, which is whether ANY orphan
// remains, not how many: each table is asked for one orphan (LIMIT 1), so a tenant that
// has one answers at the first it finds instead of counting the rest. It is bounded like the count,
// and an error (that bound included) is returned as an error, never as an answer: the caller must
// not read "could not tell" as "orphans remain" or as "none remain".
func (api *Api) HasOrphanedDeviceAttributes(ctx context.Context) (bool, error) {
	var found bool
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			// set_config(..., true) is SET LOCAL with the value bound, so the statement is a literal.
			if err := tx.Exec("SELECT set_config('statement_timeout', ?, true)", fleetOrphanProbeTimeout).Error; err != nil {
				return err
			}
		}
		var hits []uint
		if err := tx.Unscoped().Model(&EntityAttribute{}).
			Where("entity_type = ?", "device").
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attributes.entity_id)").
			Limit(1).Pluck("id", &hits).Error; err != nil {
			return err
		}
		if len(hits) != 0 {
			found = true
			return nil
		}
		if err := tx.Model(&AttributeGeneration{}).
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attribute_generations.device_id)").
			Limit(1).Pluck("id", &hits).Error; err != nil {
			return err
		}
		found = len(hits) != 0
		return nil
	})
	return found, err
}

// attributeOrphanSweepLockKey is the fixed session advisory lock that admits ONE replica to a
// sweep pass at a time. It is unrelated to the per-device attribute lock's key space.
const attributeOrphanSweepLockKey int64 = 0x6463_6f72_7068_616e // "dcorphan"

// SweepAllDeviceAttributeOrphans runs one pass on ONE replica: the replica that takes the
// sweep's session advisory lock (on a pinned connection, released at the end of the pass, or
// by the server if the connection dies) sweeps; the others skip the pass. Tenants are discovered
// from BOTH tables, because an old pod may delete every attribute and leave only generation
// heads, with a loose index scan over each table's tenant_id index, so a pass costs one index
// probe per tenant rather than a scan of the attribute tables.
func (api *Api) SweepAllDeviceAttributeOrphans(ctx context.Context) error {
	db := api.RDB.DB(core.WithSystemContext(ctx))
	if db.Dialector.Name() != "postgres" {
		return api.sweepAllDeviceAttributeOrphans(ctx)
	}
	return db.Connection(func(conn *gorm.DB) error {
		var held bool
		if err := conn.Raw("SELECT pg_try_advisory_lock(?)", attributeOrphanSweepLockKey).Scan(&held).Error; err != nil {
			return err
		}
		if !held {
			return nil // another replica is sweeping this pass
		}
		defer conn.Exec("SELECT pg_advisory_unlock(?)", attributeOrphanSweepLockKey)
		return api.sweepAllDeviceAttributeOrphans(ctx)
	})
}

func (api *Api) sweepAllDeviceAttributeOrphans(ctx context.Context) error {
	tenants, err := api.attributeOrphanTenants(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := api.SweepDeviceAttributeOrphans(core.WithTenant(ctx, tenant)); err != nil {
			failures = append(failures, fmt.Errorf("sweep device attribute orphans for tenant %q: %w", tenant, err))
		}
	}
	return errors.Join(failures...)
}

// attributeOrphanTenants lists the tenants holding attribute rows or generation heads, reading
// no row content. On PostgreSQL each table is walked one tenant at a time along an index that
// leads with tenant_id (a loose index scan); elsewhere it is a plain DISTINCT.
func (api *Api) attributeOrphanTenants(ctx context.Context) ([]string, error) {
	db := api.RDB.DB(core.WithSystemContext(ctx))
	var tenants []string
	for _, model := range []any{&EntityAttribute{}, &AttributeGeneration{}} {
		var found []string
		if db.Dialector.Name() != "postgres" {
			if err := db.Unscoped().Model(model).Distinct().Pluck("tenant_id", &found).Error; err != nil {
				return nil, err
			}
		} else {
			stmt := &gorm.Statement{DB: db}
			if err := stmt.Parse(model); err != nil {
				return nil, err
			}
			table := db.Statement.Quote(stmt.Table)
			if err := db.Raw(`WITH RECURSIVE t(tenant_id) AS (
  (SELECT tenant_id FROM ` + table + ` ORDER BY tenant_id LIMIT 1)
  UNION ALL
  SELECT (SELECT r.tenant_id FROM ` + table + ` r WHERE r.tenant_id > t.tenant_id ORDER BY r.tenant_id LIMIT 1)
  FROM t WHERE t.tenant_id IS NOT NULL
) SELECT tenant_id FROM t WHERE tenant_id IS NOT NULL`).Scan(&found).Error; err != nil {
				return nil, err
			}
		}
		tenants = append(tenants, found...)
	}
	slices.Sort(tenants)
	return slices.Compact(tenants), nil
}

// SweepDeviceAttributeOrphans performs one tenant's pass, one bounded transaction
// per table batch. These orphans are the previous-release delete window during
// a rolling upgrade; new writers protect the parent row with KEY SHARE.
func (api *Api) SweepDeviceAttributeOrphans(ctx context.Context) error {
	for _, table := range []string{"entity_attributes", "entity_attribute_generations"} {
		for {
			var removed int64
			err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
				if err := markAttributeOrphanSweep(tx); err != nil {
					return err
				}
				var err error
				removed, err = deleteAttributeOrphanBatch(tx, table)
				return err
			})
			if err != nil {
				return err
			}
			// Never count a delete whose transaction rolled back.
			deviceAttributeOrphansRemoved.WithLabelValues(table).Add(float64(removed))
			if removed < attributeOrphanBatchSize {
				break
			}
		}
	}
	return nil
}

// Dedicated destructive exception: an orphan has no device to lock. Each delete
// still runs with the transaction-local protocol marker. Do not borrow purge's
// marker helper: the source guard admits this helper only from the sweep above.
func markAttributeOrphanSweep(tx *gorm.DB) error {
	return attrprotocol.Mark(tx)
}

func deleteAttributeOrphanBatch(tx *gorm.DB, table string) (int64, error) {
	var result *gorm.DB
	switch table {
	case "entity_attributes":
		ids := tx.Unscoped().Model(&EntityAttribute{}).Select("id").
			Where("entity_type = ?", "device").
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attributes.entity_id)").
			Order("id").Limit(attributeOrphanBatchSize)
		result = tx.Unscoped().Where("id IN (?)", ids).Delete(&EntityAttribute{})
	case "entity_attribute_generations":
		ids := tx.Model(&AttributeGeneration{}).Select("id").
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attribute_generations.device_id)").
			Order("id").Limit(attributeOrphanBatchSize)
		result = tx.Where("id IN (?)", ids).Delete(&AttributeGeneration{})
	default:
		return 0, fmt.Errorf("unsupported attribute orphan table %q", table)
	}
	return result.RowsAffected, result.Error
}
