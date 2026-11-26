// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// Register before starting, so even a failed assertion after a committed start
// releases its slot. Cleanup outlives the test's request context and keeps its tenant.
func cleanupFleetTestOperation(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		op, err := api.fleetOperationByToken(cleanupCtx, token)
		if err != nil {
			t.Errorf("read bulk change %q for cleanup: %v", token, err)
			return
		}
		// Retention may already have purged the header. Completed operations need
		// no transition; aborting ABORTED or DISPATCHED is deliberately illegal.
		if op == nil || (op.Dispatch != FleetDispatchRunning && op.Dispatch != FleetDispatchPaused) {
			return
		}
		if _, err := api.AbortFleetOperation(cleanupCtx, token, "test-cleanup"); err != nil {
			t.Errorf("abort bulk change %q during cleanup: %v", token, err)
		}
	})
}

// Use system scope: the admission budget is shared by every tenant in the database.
// This check reports, rather than repairs, leaks so a missing cleanup cannot go green.
func fleetTestDispatchingError(db *gorm.DB, testName string) error {
	ctx, cancel := context.WithTimeout(core.WithSystemContext(context.Background()), 30*time.Second)
	defer cancel()
	var live []struct {
		ID       uint
		TenantId string
		Token    string
		Dispatch string
	}
	if err := db.WithContext(ctx).Model(&FleetOperation{}).
		Select("id, tenant_id, token, dispatch").
		Where("dispatch IN ?", []string{FleetDispatchRunning, FleetDispatchPaused}).Order("id").Find(&live).Error; err != nil {
		return fmt.Errorf("check dispatching bulk changes after %s: %w", testName, err)
	}
	if len(live) != 0 {
		return fmt.Errorf("%s finished with RUNNING/PAUSED bulk changes in the test database: %+v", testName, live)
	}
	return nil
}

// fleetLeakGuarded holds the tests whose first PostgreSQL manager will run the leak check.
var fleetLeakGuarded sync.Map

func checkFleetTestDispatching(t testing.TB, db *gorm.DB) {
	t.Helper()
	if err := fleetTestDispatchingError(db, t.Name()); err != nil {
		t.Error(err)
	}
}
