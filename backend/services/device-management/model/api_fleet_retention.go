// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	fleetClosedRetention    = 90 * 24 * time.Hour
	fleetOpenRetention      = 180 * 24 * time.Hour
	fleetRetentionWarning   = 14 * 24 * time.Hour
	fleetTombstoneRetention = 400 * 24 * time.Hour
	fleetRetentionExpired   = "RETENTION_EXPIRED"
)

// RetainFleetOperations runs only in the caller's tenant. Each chunk has its own
// exclusive operation fence, shared with reversal creation and posting. Re-read
// the header and reversal predicate after acquiring it, not from a stale hint.
func (api *Api) RetainFleetOperations(ctx context.Context, now time.Time) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" || core.IsSystemContext(ctx) {
		return errors.New("bulk-change retention requires a tenant")
	}
	var failures []error
	var after uint
	db := api.RDB.DB(ctx)
	operations := db.Statement.Quote(db.NamingStrategy.TableName("FleetOperation"))
	lines := db.Statement.Quote(db.NamingStrategy.TableName("FleetOperationLine"))
	postedLines := db.Model(&FleetOperationLine{}).Select("1").
		Where(lines + ".operation_id = " + operations + ".id AND " + lines + ".tenant_id = " + operations + ".tenant_id").
		Where("posting = 'POSTED'")
	for {
		var ops []FleetOperation
		// Old pods do not increment posted_line_count during a rolling upgrade,
		// so even new-pod increments could not make zero prove "never posted".
		// Check actual lines in the abort hint to avoid repeatedly taking the
		// exclusive fence for posted aborts before their warning/expiry window.
		// Eligibility is still re-read under that fence below.
		err := fleetMaintenanceProtocol(db).Select("id").Where("id > ?", after).
			Where("lines_purged_at <= ? OR (lines_purged_at IS NULL AND (pause_reason = ? OR (dispatch = 'ABORTED' AND posted_line_count = 0 AND NOT EXISTS (?)) OR (evidence = 'CLOSED' AND closed_at <= ?) OR (evidence = 'OPEN' AND created_at <= ?)))", now.Add(-fleetTombstoneRetention), fleetRetentionExpired, postedLines, now.Add(-fleetClosedRetention), now.Add(-fleetOpenRetention+fleetRetentionWarning)).
			Order("id").Limit(100).Find(&ops).Error
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if len(ops) == 0 {
			break
		}
		for _, hint := range ops {
			after = hint.ID
			var counts fleetRowCounts
			aborted := false
			err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
				counts, aborted = fleetRowCounts{}, false
				if err := fleetSetLockTimeout(tx, fleetFenceWait); err != nil {
					return err
				}
				if err := fleetFence(tx, fleetOpKey(tenant, hint.ID), false, "RETENTION"); err != nil {
					return err
				}
				var op FleetOperation
				err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, hint.ID).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				if !ledger.Drivable(ledger.DriverProtocol(op.DriverProtocol)) {
					return nil
				}
				var live int64
				if err := tx.Model(&FleetOperation{}).Where("reversal_of = ?", op.ID).
					Where("dispatch IN ('PLANNING','READY','RUNNING','PAUSED') OR evidence = 'OPEN'").Count(&live).Error; err != nil {
					return err
				}
				if live != 0 {
					return nil
				}
				if op.LinesPurgedAt.Valid {
					if now.Before(op.LinesPurgedAt.Time.Add(fleetTombstoneRetention)) {
						return nil
					}
					return fleetDeleteTombstone(tx, &op, counts)
				}
				if op.Evidence == "OPEN" && !now.Before(op.CreatedAt.Add(fleetOpenRetention-fleetRetentionWarning)) {
					var n int64
					if err := tx.Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind = 'RETENTION_WARNING'", op.ID).Count(&n).Error; err != nil {
						return err
					}
					if n == 0 {
						if err := tx.Create(&FleetOperationEvent{OperationId: int64(op.ID), At: now, Kind: "RETENTION_WARNING"}).Error; err != nil {
							return err
						}
					}
				}
				var posted int64
				if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = 'POSTED'", op.ID).Count(&posted).Error; err != nil {
					return err
				}
				neverPosted := op.Dispatch == "ABORTED" && op.PostedLineCount == 0 && posted == 0
				closedDue := op.Evidence == "CLOSED" && op.ClosedAt.Valid && !now.Before(op.ClosedAt.Time.Add(fleetClosedRetention))
				// A durable marker carries an OPEN expiry across chunks. The
				// reconciler may close evidence as soon as the lines disappear;
				// that must not restart a 90-day wait for remaining line events.
				openDue := op.PauseReason == fleetRetentionExpired || (op.Evidence == "OPEN" && !now.Before(op.CreatedAt.Add(fleetOpenRetention)))
				if !neverPosted && !closedDue && !openDue {
					return nil
				}
				// Fence off a still-dispatching operation before removing its work.
				if openDue && op.PauseReason != fleetRetentionExpired {
					changes := map[string]any{"pause_reason": fleetRetentionExpired}
					if op.Dispatch != "DISPATCHED" && op.Dispatch != "ABORTED" {
						changes["dispatch"] = "ABORTED"
						changes["execution_epoch"] = op.ExecutionEpoch + 1
						changes["dispatched_at"] = now
						changes["state_changed_at"] = now
						changes["state_changed_by"] = ""
						aborted = true
					}
					if err := tx.Model(&op).Updates(changes).Error; err != nil {
						return err
					}
					if err := tx.Create(&FleetOperationEvent{OperationId: int64(op.ID), At: now, Kind: "EXPIRE", Reason: fleetRetentionExpired}).Error; err != nil {
						return err
					}
				}
				if err := fleetPurgeEvidenceChunk(tx, op.ID, counts); err != nil {
					return err
				}
				var remaining int64
				if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Count(&remaining).Error; err != nil {
					return err
				}
				if remaining != 0 {
					return nil
				}
				if err := tx.Model(&FleetOperationEvent{}).Where("operation_id = ? AND (line_id IS NOT NULL OR kind IN ('RESOLUTION','VERDICT','CONTRADICTING_EVIDENCE','OVERRIDE_CHANGED','REDACTED'))", op.ID).Count(&remaining).Error; err != nil {
					return err
				}
				if remaining != 0 {
					return nil
				}
				if err := tx.Model(&FleetOperationContract{}).Where("operation_id = ? AND body <> ''", op.ID).Count(&remaining).Error; err != nil {
					return err
				}
				if remaining != 0 {
					return nil
				}
				changes := map[string]any{"lines_purged_at": now, "evidence": "CLOSED"}
				if !op.ClosedAt.Valid {
					changes["closed_at"] = now
				}
				return tx.Model(&op).Updates(changes).Error
			})
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if aborted {
				bulkMetrics.transition(FleetDispatchAborted, fleetRetentionExpired)
			}
			for table, n := range counts {
				bulkMetrics.maintained(bulkMetrics.deleted, table, n)
			}
		}
	}
	return errors.Join(failures...)
}

func fleetPurgeEvidenceChunk(tx *gorm.DB, id uint, counts fleetRowCounts) error {
	lines := tx.Model(&FleetOperationLine{}).Select("id").Where("operation_id = ?", id).Order("id").Limit(fleetMaintenanceChunk)
	res := tx.Where("id IN (?)", lines).Delete(&FleetOperationLine{})
	if res.Error != nil {
		return res.Error
	}
	counts.add("lines", res.RowsAffected)
	events := tx.Model(&FleetOperationEvent{}).Select("id").Where("operation_id = ? AND (line_id IS NOT NULL OR kind IN ('RESOLUTION','VERDICT','CONTRADICTING_EVIDENCE','OVERRIDE_CHANGED','REDACTED'))", id).Order("id").Limit(fleetMaintenanceChunk)
	res = tx.Where("id IN (?)", events).Delete(&FleetOperationEvent{})
	if res.Error != nil {
		return res.Error
	}
	counts.add("events", res.RowsAffected)
	contracts := tx.Model(&FleetOperationContract{}).Select("id").Where("operation_id = ? AND body <> ''", id).Order("id").Limit(fleetMaintenanceChunk)
	return tx.Model(&FleetOperationContract{}).Where("id IN (?)", contracts).Update("body", "").Error
}

func fleetDeleteTombstone(tx *gorm.DB, op *FleetOperation, counts fleetRowCounts) error {
	for _, step := range []struct {
		model any
		table string
	}{{&FleetOperationEvent{}, "events"}, {&FleetOperationContract{}, "contracts"}} {
		model := step.model
		ids := tx.Model(model).Select("id").Where("operation_id = ?", op.ID).Order("id").Limit(fleetMaintenanceChunk)
		res := tx.Where("id IN (?)", ids).Delete(model)
		if res.Error != nil {
			return res.Error
		}
		counts.add(step.table, res.RowsAffected)
		var n int64
		if err := tx.Model(model).Where("operation_id = ?", op.ID).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return nil
		}
	}
	res := tx.Where("operation_id = ?", op.ID).Delete(&FleetOperationRuntime{})
	if res.Error != nil {
		return res.Error
	}
	counts.add("runtime", res.RowsAffected)
	res = tx.Delete(op)
	counts.add("operations", res.RowsAffected)
	return res.Error
}

// Like the expiry sweep's cursor, this is advisory, process-local state. A new
// replica starts at the beginning; an interrupted pass resumes after its last
// attempted tenant, so one tenant cannot consume every pass's budget.
type fleetMaintenanceCursor struct {
	sync.Mutex
	after string
}

// MaintainFleetOperations is a nonblocking singleton per pass, not a leader.
// Discovery is the shared tenant-ID listing, so no row content crosses tenants
// and no new isolation bypass is needed; repair and retention each run under
// that tenant's own context and apply their own driver-protocol filter.
func (api *Api) MaintainFleetOperations(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pass := func() error {
		tenants, err := api.RDB.TenantsWith(ctx, &FleetOperation{})
		if err != nil {
			return err
		}
		api.fleetMaintenanceCursor.Lock()
		after := api.fleetMaintenanceCursor.after
		api.fleetMaintenanceCursor.Unlock()
		if i := slices.Index(tenants, after); i >= 0 {
			tenants = append(tenants[i+1:], tenants[:i+1]...)
		}
		// If the cursor's tenant disappeared, restart in discovery order. With
		// a stable tenant set and budget to start at least one tenant per pass,
		// every tenant is attempted within at most len(tenants) acquired passes.
		_, err = core.ForEachTenant(ctx, tenants, func(tctx context.Context, tenant string) error {
			// Advance before work: repair or retention can exhaust the deadline.
			api.fleetMaintenanceCursor.Lock()
			api.fleetMaintenanceCursor.after = tenant
			api.fleetMaintenanceCursor.Unlock()
			repairErr := api.RepairFleetErasure(tctx)
			now, err := fleetMaintenanceNow(api.RDB.DB(tctx))
			if err != nil {
				return errors.Join(repairErr, err)
			}
			return errors.Join(repairErr, api.RetainFleetOperations(tctx, now))
		})
		return err
	}
	if api.RDB.Database.Dialector.Name() != "postgres" {
		return pass()
	}
	return api.RDB.SingletonPass(ctx, "device-management-bulk-change-maintenance", pass)
}
