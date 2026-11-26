// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// I-13: a PAUSED operation already occupies a dispatch slot. Its concurrent resume must
// succeed while exactly one of two starts takes the last slot, in either lock order.
func TestFleetAdmissionConcurrentStartsAndResumeAtLastSlot(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 1)
	for round := 0; round < 6; round++ {
		ops := make([]*FleetOperation, 3)
		for i := range ops {
			var err error
			ops[i], err = api.CreateFleetOperation(ctx, setReq(fmt.Sprintf("mixed-%d-%d", round, i), tokens...), "alice")
			require.NoError(t, err)
			cleanupFleetTestOperation(t, api, ctx, ops[i].Token)
		}
		_, err := api.StartFleetOperation(ctx, ops[0].Token, ops[0].ContractDigest, alice)
		require.NoError(t, err)
		_, err = api.PauseFleetOperation(ctx, ops[0].Token, alice.Name)
		require.NoError(t, err)
		errs := make([]error, len(ops))
		barrier := make(chan struct{})
		var wg sync.WaitGroup
		for i := range ops {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-barrier
				_, errs[i] = admissionCall(api, ctx, ops[i], i == 0)
			}(i)
		}
		close(barrier)
		wg.Wait()
		require.NoError(t, errs[0], "resuming does not consume an additional slot")
		won := 0
		for _, err := range errs[1:] {
			if err == nil {
				won++
			} else {
				requireAdmissionQuota(t, err, "tenant.dispatchingOperations")
			}
		}
		require.Equal(t, 1, won, "exactly one start takes the last slot: %v", errs)
		var dispatching int64
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).
			Where("dispatch IN ?", []string{FleetDispatchRunning, FleetDispatchPaused}).Count(&dispatching).Error)
		require.Equal(t, int64(2), dispatching)
		for _, op := range ops {
			_, err := api.AbortFleetOperation(ctx, op.Token, alice.Name)
			require.NoError(t, err)
		}
	}
}

func TestFleetAdmissionScansOutsideFenceAndCountsUnderBothLocks(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			api, ctx, tenant, tokens := fleetPGFixture(t, 1)
			op, err := api.CreateFleetOperation(ctx, setReq("locks", tokens...), "alice")
			require.NoError(t, err)
			cleanupFleetTestOperation(t, api, ctx, op.Token)
			if resume {
				_, err = api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
				require.NoError(t, err)
				_, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
				require.NoError(t, err)
			}
			orphans, evidence := 0, 0
			db := api.RDB.DB(ctx)
			require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:admission-locks", func(tx *gorm.DB) {
				if tx.Statement.Table == "entity_attributes" || tx.Statement.Table == "entity_attribute_generations" {
					orphans++
					// A separate connection can take every lock while the 5s scan runs.
					require.NoError(t, api.RDB.DB(ctx).Transaction(func(probe *gorm.DB) error {
						for _, key := range []int64{fleetOpKey(tenant, op.ID), fleetAdmissionTenantKey(tenant), fleetAdmissionInstanceKey()} {
							var free bool
							if err := probe.Raw("SELECT pg_try_advisory_xact_lock(?)", key).Scan(&free).Error; err != nil {
								return err
							}
							require.True(t, free, "orphan scan must precede K_op and K_adm")
						}
						return nil
					}))
				}
				if tx.Statement.Table != "fleet_operation_lines" || !strings.Contains(tx.Statement.SQL.String(), "settlement IS NULL") {
					return
				}
				evidence++
				for _, key := range []int64{fleetAdmissionTenantKey(tenant), fleetAdmissionInstanceKey()} {
					var held int64
					// Inspect the very connection that counted, not a fresh pool connection.
					// Ask the statement's own connection pool directly: a gorm session opened
					// inside a callback inherits the outer statement's bound variables.
					require.NoError(t, tx.Statement.ConnPool.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM pg_locks
						WHERE pid = pg_backend_pid() AND locktype = 'advisory' AND granted
						AND classid::bigint = %d AND objid::bigint = %d`, uint64(key)>>32, uint64(key)&0xffffffff)).Scan(&held))
					require.Equal(t, int64(1), held, "evidence count must hold both admission locks on its transaction")
				}
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("test:admission-locks")) })
			_, err = admissionCall(api, ctx, op, resume)
			require.NoError(t, err)
			require.Equal(t, 2, orphans)
			require.Equal(t, 2, evidence, "tenant and instance counts")
			_, err = api.AbortFleetOperation(ctx, op.Token, alice.Name)
			require.NoError(t, err)
		})
	}
}
