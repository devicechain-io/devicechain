// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	fleetDriverOperationsPerPass = 10
	fleetDriverPostsPerReplica   = 4
	fleetDriverPassTimeout       = 45 * time.Second
)

// State belongs to this Api (one service replica), never to the cluster or a tenant.
type fleetDriverState struct {
	sync.Mutex
	after string
	posts chan struct{}
}

type fleetDriverCandidate struct {
	TenantID string `gorm:"column:tenant_id"`
	Token    string
}

// Rank each tenant's operations before ordering tenants after the cursor. A tenant with many
// operations cannot fill the bounded list ahead of another tenant's first operation.
// Only tenant/token hints cross the system scope (a declared isolation bypass, core/test
// sanctionedSystemContexts); Drive rechecks each header and writes only in tenant scope.
func (api *Api) fleetDriverCandidates(ctx context.Context) ([]fleetDriverCandidate, error) {
	state := &api.fleetDriver
	state.Lock()
	defer state.Unlock()
	db := api.RDB.DB(core.WithSystemContext(ctx))
	running := db.Model(&FleetOperation{}).Scopes(fleetDrivableScope).
		Select("fleet_operations.tenant_id, token, row_number() OVER (PARTITION BY fleet_operations.tenant_id ORDER BY runtime.driver_seen_at NULLS FIRST, fleet_operations.id) AS tenant_rank").
		Joins("LEFT JOIN fleet_operation_runtime AS runtime ON runtime.operation_id = fleet_operations.id AND runtime.tenant_id = fleet_operations.tenant_id").
		Where("dispatch = ?", FleetDispatchRunning)
	var candidates []fleetDriverCandidate
	err := db.Table("(?) AS runnable", running).Select("tenant_id, token").
		Order(clause.OrderBy{Expression: gorm.Expr("tenant_rank, CASE WHEN tenant_id > ? THEN 0 ELSE 1 END, tenant_id", state.after)}).
		Limit(fleetDriverOperationsPerPass).Scan(&candidates).Error
	if err == nil && len(candidates) > 0 {
		state.after = candidates[len(candidates)-1].TenantID
	}
	return candidates, err
}

// acquireFleetDriverPost shares four slots across every operation and overlapping pass on a replica.
func (api *Api) acquireFleetDriverPost(ctx context.Context) (func(), error) {
	state := &api.fleetDriver
	state.Lock()
	if state.posts == nil {
		state.posts = make(chan struct{}, fleetDriverPostsPerReplica)
	}
	posts := state.posts
	state.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case posts <- struct{}{}:
		return func() { <-posts }, nil
	}
}

// RunFleetDriver runs one bounded pass on every replica. No singleton: the durable claims and
// pacer already arbitrate replicas. Independent operations run together so a large operation
// cannot consume the whole pass before another tenant starts. Shutdown cancels discovery,
// standing requests, slot waits and all posts, then waits for their workers to return.
func (api *Api) RunFleetDriver(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fleetDriverPassTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	candidates, err := api.fleetDriverCandidates(ctx)
	if err != nil {
		return err
	}
	errs := make([]error, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		run := func() {
			_, errs[i] = api.DriveFleetOperation(core.WithTenant(ctx, candidate.TenantID), candidate.Token, DefaultFleetCaps)
		}
		if api.RDB.Database.Dialector.Name() != "postgres" {
			run() // SQLite fixtures allow one writer at a time.
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); run() }()
	}
	wg.Wait()
	return errors.Join(append(errs, ctx.Err())...)
}
