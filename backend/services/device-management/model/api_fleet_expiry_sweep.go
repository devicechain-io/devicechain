// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sync"
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

// fleetExpiryCursor is the last tenant a pass visited. Discovery resumes after it and wraps, so a
// tenant whose expiry keeps failing cannot hold one of the ten places in every pass. It lives in
// memory: a replica that takes the singleton over starts from the beginning, which costs one pass.
type fleetExpiryCursor struct {
	sync.Mutex
	after string
}

func fleetExpiryCandidates(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Scopes(fleetDrivableScope).
		Where("(dispatch = ? AND expires_at IS NOT NULL AND expires_at <= ?) OR (dispatch = ? AND created_at <= ?)",
			FleetDispatchReady, now, FleetDispatchPlanning, now.Add(-ledger.PlanningAbandonedAfter))
}

// fleetExpiryTenants discovers at most ten tenants with due operations, after the cursor first and
// then wrapping to the beginning. Only tenant IDs cross scopes. Due is judged on the database's clock.
func (api *Api) fleetExpiryTenants(ctx context.Context) ([]string, error) {
	system := core.WithSystemContext(ctx)
	now, err := fleetDBNow(api.RDB.DB(system))
	if err != nil {
		return nil, err
	}
	api.fleetExpiryCursor.Lock()
	after := api.fleetExpiryCursor.after
	api.fleetExpiryCursor.Unlock()
	discover := func(where string, limit int) ([]string, error) {
		var tenants []string
		err := fleetExpiryCandidates(api.RDB.DB(system).Model(&FleetOperation{}), now).
			Where(where, after).Distinct("tenant_id").Order("tenant_id").Limit(limit).Pluck("tenant_id", &tenants).Error
		return tenants, err
	}
	tenants, err := discover("tenant_id > ?", fleetExpiryTenantsPerPass)
	if err != nil {
		return nil, err
	}
	if len(tenants) < fleetExpiryTenantsPerPass && after != "" {
		wrapped, err := discover("tenant_id <= ?", fleetExpiryTenantsPerPass-len(tenants))
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, wrapped...)
	}
	return tenants, nil
}

// SweepExpiredFleetOperations expires at most 100 operations for each of at most 10 tenants.
// It never drives RUNNING operations. Like the outbox sweeps it uses the shared nonblocking
// singleton primitive; the per-operation control fence also makes overlapping passes safe if
// the lock's connection dies. Shutdown cancels the pass, including lock waits and DB calls.
func (api *Api) SweepExpiredFleetOperations(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fleetExpiryPassTimeout)
	defer cancel()
	pass := func() error {
		// Operation reads and transitions below use a fresh tenant context derived from the
		// original non-system context.
		tenants, err := api.fleetExpiryTenants(ctx)
		if err != nil {
			return err
		}
		failed := 0
		var lastErr error
		for _, tenant := range tenants {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := api.ExpireFleetOperations(core.WithTenant(ctx, tenant)); err != nil {
				failed++
				lastErr = err
			}
			api.fleetExpiryCursor.Lock()
			api.fleetExpiryCursor.after = tenant
			api.fleetExpiryCursor.Unlock()
		}
		return core.PassResult(len(tenants), failed, lastErr)
	}
	if api.RDB.Database.Dialector.Name() != "postgres" {
		return pass() // SQLite exercises the bounded pass; advisory locks are PostgreSQL-only.
	}
	return api.RDB.SingletonPass(ctx, fleetExpirySweepLockName, pass)
}
