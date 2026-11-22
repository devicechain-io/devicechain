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

// CountOrphanedDeviceAttributes is the tenant-scoped readiness probe. Include
// soft-deleted attributes: they remain physical rows until this cleanup deletes them.
func (api *Api) CountOrphanedDeviceAttributes(ctx context.Context) (int64, error) {
	var count int64
	err := api.RDB.DB(ctx).Unscoped().Model(&EntityAttribute{}).
		Where("entity_type = ?", "device").
		Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.id = entity_attributes.entity_id)").
		Count(&count).Error
	return count, err
}

// SweepAllDeviceAttributeOrphans discovers tenants from BOTH tables: an old pod
// may delete every attribute but leave only generation heads. Every replica runs
// this idempotent sweep; concurrent deletes count only rows actually committed.
func (api *Api) SweepAllDeviceAttributeOrphans(ctx context.Context) error {
	var tenants, headTenants []string
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	if err := sys.Unscoped().Model(&EntityAttribute{}).Where("entity_type = ?", "device").
		Distinct().Pluck("tenant_id", &tenants).Error; err != nil {
		return err
	}
	if err := sys.Model(&AttributeGeneration{}).Distinct().Pluck("tenant_id", &headTenants).Error; err != nil {
		return err
	}
	tenants = append(tenants, headTenants...)
	slices.Sort(tenants)
	tenants = slices.Compact(tenants)
	var failures []error
	for _, tenant := range tenants {
		if err := api.SweepDeviceAttributeOrphans(core.WithTenant(ctx, tenant)); err != nil {
			failures = append(failures, fmt.Errorf("sweep device attribute orphans for tenant %q: %w", tenant, err))
		}
	}
	return errors.Join(failures...)
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
