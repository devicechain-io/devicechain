// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	configurationReportRetention  = 200 * 24 * time.Hour
	configurationReportPruneBatch = 1000
)

var configurationReportsPruned = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "device_configuration_reports_pruned_total",
	Help: "Device configuration verdict history rows removed by reason.",
}, []string{"reason"})

func init() {
	configurationReportsPruned.WithLabelValues("orphan")
	configurationReportsPruned.WithLabelValues("retention")
}

// MaintainDeviceConfigurationReports repairs deletes from older pods and bounds history to 200
// days. Every replica may run it: each DELETE re-checks its predicate and counts only the rows it
// removed, so two replicas overlapping remove each row once.
//
// 🔴 ANTI-JOINS, NOT NOT IN. An orphan is a report whose device row is gone; it is found with
// NOT EXISTS against devices by primary key, which PostgreSQL plans as an anti-join. A
// `NOT IN (SELECT id FROM devices)` degrades to a quadratic scan once the subquery outgrows
// work_mem, and this runs on every replica.
//
// 🔴 RETENTION KEEPS THE CURRENT VERDICT. The row matching the state's captured (revision,
// status) is never pruned, however old: deleting it would let the next identical report
// re-insert it with a new receive time, breaking first-verdict-wins for the revision the device
// still runs.
func (api *Api) MaintainDeviceConfigurationReports(ctx context.Context) error {
	tenants, err := api.configurationReportTenants(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-configurationReportRetention)
	var failures []error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		db := api.RDB.DB(core.WithTenant(ctx, tenant))
		q := func(model string) string { return db.Statement.Quote(db.NamingStrategy.TableName(model)) }
		reports, states := q("DeviceConfigurationReport"), q("DeviceConfigurationState")
		devices := db.Unscoped().Model(&Device{}).Select("1").Where(q("Device") + ".id = " + reports + ".device_id")
		current := db.Model(&DeviceConfigurationState{}).Select("1").
			Where(states + ".device_id = " + reports + ".device_id").
			Where(states + ".captured_revision = " + reports + ".revision").
			Where(states + ".captured_status = " + reports + ".status")
		for _, reason := range []string{"orphan", "retention"} {
			for {
				eligible := db.Model(&DeviceConfigurationReport{})
				if reason == "orphan" {
					eligible = eligible.Where("NOT EXISTS (?)", devices).Order("id")
				} else {
					eligible = eligible.Where("created_at < ?", cutoff).Where("NOT EXISTS (?)", current).Order("created_at").Order("id")
				}
				ids := eligible.Select("id").Limit(configurationReportPruneBatch)
				result := db.Where("id IN (?)", ids).Delete(&DeviceConfigurationReport{})
				if result.Error != nil {
					failures = append(failures, result.Error)
					break
				}
				configurationReportsPruned.WithLabelValues(reason).Add(float64(result.RowsAffected))
				if result.RowsAffected == 0 {
					break
				}
			}
		}
	}
	return errors.Join(failures...)
}

// configurationReportTenants lists the tenants that hold report history, reading no row
// content. On PostgreSQL it walks the (tenant_id, created_at) index one tenant at a time (a
// loose index scan), so the cost follows the number of tenants rather than the number of
// reports; elsewhere it is a plain DISTINCT.
func (api *Api) configurationReportTenants(ctx context.Context) ([]string, error) {
	db := api.RDB.DB(core.WithSystemContext(ctx))
	var tenants []string
	if db.Dialector.Name() != "postgres" {
		err := db.Model(&DeviceConfigurationReport{}).Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
		return tenants, err
	}
	table := db.Statement.Quote(db.NamingStrategy.TableName("DeviceConfigurationReport"))
	err := db.Raw(`WITH RECURSIVE t(tenant_id) AS (
  (SELECT tenant_id FROM ` + table + ` ORDER BY tenant_id LIMIT 1)
  UNION ALL
  SELECT (SELECT r.tenant_id FROM ` + table + ` r WHERE r.tenant_id > t.tenant_id ORDER BY r.tenant_id LIMIT 1)
  FROM t WHERE t.tenant_id IS NOT NULL
) SELECT tenant_id FROM t WHERE tenant_id IS NOT NULL`).Scan(&tenants).Error
	return tenants, err
}
