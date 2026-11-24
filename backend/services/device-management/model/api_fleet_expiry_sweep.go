// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
)

const (
	fleetExpiryTenantsPerPass = 10
	fleetExpiryPassTimeout    = 45 * time.Second
	fleetExpirySweepLockName  = "device-management-fleet-expiry-sweep"
)

func fleetExpiryCandidates(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Where("driver_protocol >= ? AND driver_protocol <= ?", 1, ledger.SupportedDriverProtocol).
		Where("(dispatch = ? AND expires_at IS NOT NULL AND expires_at <= ?) OR (dispatch = ? AND created_at <= ?)",
			FleetDispatchReady, now, FleetDispatchPlanning, now.Add(-ledger.PlanningAbandonedAfter))
}

// SweepExpiredFleetOperations expires at most 100 operations for each of at most 10 tenants.
// It never drives RUNNING operations. Like the outbox sweeps it uses the shared nonblocking
// singleton primitive; the per-operation control fence also makes overlapping passes safe if
// the lock's connection dies. Shutdown cancels the pass, including lock waits and DB calls.
func (api *Api) SweepExpiredFleetOperations(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fleetExpiryPassTimeout)
	defer cancel()
	pass := func() error {
		now := time.Now()
		var tenants []string
		// Only tenant IDs cross scopes; operation reads and transitions below use a fresh
		// tenant context derived from the original non-system context.
		err := fleetExpiryCandidates(api.RDB.DB(core.WithSystemContext(ctx)).Model(&FleetOperation{}), now).
			Distinct("tenant_id").Order("tenant_id").Limit(fleetExpiryTenantsPerPass).Pluck("tenant_id", &tenants).Error
		if err != nil {
			return err
		}
		failed := 0
		var lastErr error
		for _, tenant := range tenants {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := api.ExpireFleetOperations(core.WithTenant(ctx, tenant), now); err != nil {
				failed++
				lastErr = err
			}
		}
		return core.PassResult(len(tenants), failed, lastErr)
	}
	if api.RDB.Database.Dialector.Name() != "postgres" {
		return pass() // SQLite exercises the bounded pass; advisory locks are PostgreSQL-only.
	}
	return api.RDB.SingletonPass(ctx, fleetExpirySweepLockName, pass)
}
