// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The driver pass: what one replica does for one RUNNING operation each time it looks. Nothing in it is
// leader-elected; every replica may run it, and the posting transaction's claim is what keeps two of them
// from applying one line twice. It is not started by anything yet: the loops start together with the engine,
// and must not run while nothing is served (ledger.Served).
//
//	1  a LEASE: a fresh, uncached answer from user-management that the grantee still holds the authorities,
//	   good for five seconds from the moment the question was SENT (a slow answer does not stretch it)
//	2  the thresholds: a failure or ambiguity count at its limit pauses the operation
//	3  PERMITS: how many posting attempts this operation may start in this second, taken from the database
//	4  the HINTS: the due PLANNED lines in ordinal order (an unlocked read, no claim)
//	5  the posts, a few at a time, each a PostFleetLine
//	6  the completion: nothing left PLANNED means the operation is DISPATCHED

// Pause reasons the driver writes, and the holds it reports (a hold waits; it never pauses).
const (
	FleetPauseAuthorityRevoked = "AUTHORITY_REVOKED"
	FleetPauseFailureThreshold = "FAILURE_THRESHOLD"
	FleetPauseAmbiguity        = "AMBIGUITY_THRESHOLD"

	FleetHoldAuthorityUnavailable = "AUTHORITY_UNAVAILABLE"
	FleetHoldNotActivated         = "NOT_ACTIVATED"
	FleetHoldRateLimit            = "RATE_LIMIT"

	fleetEventAutoPause  = "AUTO_PAUSE"
	fleetEventDispatched = "DISPATCHED"

	// FleetLeaseTTL is how long a standing answer lets lines commit, from the moment the question was sent.
	FleetLeaseTTL = 5 * time.Second
	// FleetStandingTimeout bounds the question itself.
	FleetStandingTimeout = 4 * time.Second
	// FleetDriverWorkers is how many lines one pass posts at once.
	FleetDriverWorkers = 4
)

// FleetCaps are the budgets a pass shares with the other operations that are running.
type FleetCaps struct {
	TenantPostsPerSecond   int
	InstancePostsPerSecond int
}

// DefaultFleetCaps are the constants of this release.
var DefaultFleetCaps = FleetCaps{TenantPostsPerSecond: ledger.TenantPostsPerSecond, InstancePostsPerSecond: ledger.InstancePostsPerSecond}

// FleetDriveResult says what a pass did.
type FleetDriveResult struct {
	Posted, Refused, Busy, LostRace, Retried int
	// Hold is set when the pass waited for something (it did not pause the operation).
	Hold string
	// Paused is the reason the pass paused the operation, if it did.
	Paused string
	// Dispatched is true when the pass finished the operation's posting.
	Dispatched bool
}

var fleetStandingAuthorities = []string{string("device:write"), string("fleet:operate")}

// DriveFleetOperation runs one pass for one operation. A token that names no RUNNING operation is a no-op.
func (api *Api) DriveFleetOperation(ctx context.Context, token string, caps FleetCaps) (*FleetDriveResult, error) {
	res := &FleetDriveResult{}
	op, err := api.fleetOperationByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if op == nil || op.Dispatch != FleetDispatchRunning {
		return res, nil
	}
	api.touchFleetRuntime(ctx, op, nil, nil) // the heartbeat alone: a hold is not cleared by looking

	// 1. The lease.
	lease, hold, err := api.fleetLease(ctx, op)
	if err != nil {
		return nil, err
	}
	if hold != "" {
		res.Hold = hold
		api.touchFleetRuntime(ctx, op, nil, &hold)
		return res, nil
	}
	if lease == nil {
		// A definite "no": the grantee no longer holds the authorities. Nothing posted is undone.
		if _, perr := api.fleetAutoPause(ctx, token, op.ExecutionEpoch, FleetPauseAuthorityRevoked); perr != nil && !isStaleDecision(perr) {
			return nil, perr
		}
		res.Paused = FleetPauseAuthorityRevoked
		return res, nil
	}

	// 2. The thresholds.
	db := api.RDB.DB(ctx)
	if op.EffFailureThreshold.Valid {
		n, err := fleetFailureCount(db, op)
		if err != nil {
			return nil, err
		}
		if n >= int64(op.EffFailureThreshold.Int32) {
			return api.pauseDriven(ctx, res, op, FleetPauseFailureThreshold)
		}
	}
	if op.EffAmbiguityThreshold.Valid {
		n, err := fleetAmbiguityCount(db, op)
		if err != nil {
			return nil, err
		}
		if n >= int64(op.EffAmbiguityThreshold.Int32) {
			return api.pauseDriven(ctx, res, op, FleetPauseAmbiguity)
		}
	}

	// 3-4. Permits, then the due lines.
	now := fleetClock()
	var planned int64
	if err := db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPlanned).Count(&planned).Error; err != nil {
		return nil, err
	}
	if planned == 0 {
		return api.finishDriven(ctx, res, op)
	}
	allowed, err := api.fleetAllowed(ctx, op, caps)
	if err != nil {
		return nil, err
	}
	var due []FleetOperationLine
	if err := db.Select("id", "ordinal").Where("operation_id = ? AND posting = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)",
		op.ID, FleetPostingPlanned, now).Order("ordinal").Limit(allowed).Find(&due).Error; err != nil {
		return nil, err
	}
	if len(due) == 0 {
		return res, nil // every remaining line is backing off
	}
	granted, err := api.AcquireFleetPermits(ctx, op.ID, len(due), allowed, now)
	if err != nil {
		return nil, err
	}
	if granted == 0 {
		res.Hold = FleetHoldRateLimit
		rl := FleetHoldRateLimit
		api.touchFleetRuntime(ctx, op, nil, &rl)
		return res, nil
	}
	due = due[:granted]

	// 5. The posts.
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := FleetDriverWorkers
	if db.Dialector.Name() != "postgres" {
		workers = 1 // the SQLite fixtures take one writer at a time
	}
	sem := make(chan struct{}, workers)
	var stop bool
	var firstErr error
	for _, line := range due {
		mu.Lock()
		halted := stop || firstErr != nil
		mu.Unlock()
		if halted {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(lineID uint) {
			defer wg.Done()
			defer func() { <-sem }()
			r, perr := api.PostFleetLine(ctx, op.ID, lineID, *lease)
			mu.Lock()
			defer mu.Unlock()
			if perr != nil {
				if firstErr == nil {
					firstErr = perr
				}
				return
			}
			switch r.Outcome {
			case FleetPosted:
				res.Posted++
			case FleetPostRefused:
				res.Refused++
			case FleetPostBusy:
				res.Busy++
			case FleetPostLostRace:
				res.LostRace++
			case FleetPostRetry:
				res.Retried++
			case FleetPostNotActivated:
				res.Hold, stop = FleetHoldNotActivated, true
			case FleetPostFenceRefused, FleetPostLeaseExpired, FleetPostFenceBusy:
				stop = true
			}
		}(line.ID)
	}
	wg.Wait()
	if firstErr != nil {
		return res, firstErr
	}
	posted := res.Posted > 0
	api.touchFleetRuntime(ctx, op, &posted, &res.Hold)

	// 6. Completion.
	if res.Hold == "" && !stop {
		if err := db.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPlanned).Count(&planned).Error; err != nil {
			return res, err
		}
		if planned == 0 {
			return api.finishDriven(ctx, res, op)
		}
	}
	return res, nil
}

func isStaleDecision(err error) bool {
	var fe *FleetOperationError
	return errors.As(err, &fe) && (fe.Code == FleetCodeIllegalTransition || fe.Code == FleetCodeContractWidened)
}

// fleetLease asks for a fresh standing answer. It returns a lease when the grantee still holds the
// authorities, nil with no hold for a definite no, and a hold for anything that is not an answer (an error,
// a timeout, no checker wired): an unavailable answer is never a pass and never a pause.
func (api *Api) fleetLease(ctx context.Context, op *FleetOperation) (*FleetLease, string, error) {
	if api.PrincipalStanding == nil {
		return nil, FleetHoldAuthorityUnavailable, nil
	}
	askCtx, cancel := context.WithTimeout(ctx, FleetStandingTimeout)
	defer cancel()
	sent := time.Now() // the lease counts from when the question was SENT, on this process's monotonic clock
	answer, err := api.PrincipalStanding.PrincipalStanding(askCtx, []string{op.Grantee}, fleetStandingAuthorities)
	if err != nil || answer == nil {
		return nil, FleetHoldAuthorityUnavailable, nil
	}
	qualifies, present := answer[op.Grantee]
	if !present {
		return nil, FleetHoldAuthorityUnavailable, nil
	}
	if !qualifies {
		return nil, "", nil
	}
	return &FleetLease{Epoch: op.ExecutionEpoch, Grantee: op.Grantee, Deadline: sent.Add(FleetLeaseTTL)}, "", nil
}

// fleetAllowed is the most posting attempts this operation may start in a second: its own effective rate,
// the tenant's cap shared among the tenant's running operations, and the instance's shared among the instance's.
func (api *Api) fleetAllowed(ctx context.Context, op *FleetOperation, caps FleetCaps) (int, error) {
	var tenantRunning, instanceRunning int64
	if err := api.RDB.DB(ctx).Model(&FleetOperation{}).Where("dispatch = ?", FleetDispatchRunning).Count(&tenantRunning).Error; err != nil {
		return 0, err
	}
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&FleetOperation{}).Where("dispatch = ?", FleetDispatchRunning).Count(&instanceRunning).Error; err != nil {
		return 0, err
	}
	allowed := int(op.EffPostsPerSecond)
	if t := caps.TenantPostsPerSecond / int(max(tenantRunning, 1)); t < allowed {
		allowed = t
	}
	if i := caps.InstancePostsPerSecond / int(max(instanceRunning, 1)); i < allowed {
		allowed = i
	}
	return max(allowed, 1), nil
}

// AcquireFleetPermits takes up to want posting permits for one operation from its one-second window, under
// the runtime row's lock so replicas share the count. A permit is one posting ATTEMPT, whatever its outcome,
// so the pacing bounds lock traffic and not only successful posts. It returns how many it granted.
func (api *Api) AcquireFleetPermits(ctx context.Context, opID uint, want, allowed int, now time.Time) (int, error) {
	granted := 0
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var rt FleetOperationRuntime
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", opID).First(&rt).Error; err != nil {
			return err
		}
		issued := int(rt.PaceIssued)
		start := rt.PaceWindowStart
		if !start.Valid || !start.Time.After(now.Add(-time.Second)) {
			start, issued = sql.NullTime{Time: now, Valid: true}, 0
		}
		granted = max(min(want, allowed-issued), 0)
		return tx.Model(&FleetOperationRuntime{}).Where("operation_id = ?", opID).
			Updates(map[string]any{"pace_window_start": start, "pace_issued": issued + granted}).Error
	})
	return granted, err
}

// touchFleetRuntime records the driver's heartbeat, progress and hold with single autocommit statements:
// it never runs inside a transaction that holds another lock, so it sits outside the lock order.
func (api *Api) touchFleetRuntime(ctx context.Context, op *FleetOperation, progressed *bool, hold *string) {
	now := fleetClock()
	changes := map[string]any{"driver_seen_at": sql.NullTime{Time: now, Valid: true}}
	if hold != nil {
		changes["hold_code"] = *hold
		changes["hold_seen_at"] = sql.NullTime{Time: now, Valid: *hold != ""}
		if *hold != "" {
			var rt FleetOperationRuntime
			// hold_since is when THIS hold began: it moves only when the code changes.
			if err := api.RDB.DB(ctx).Select("hold_code", "hold_since").Where("operation_id = ?", op.ID).First(&rt).Error; err == nil && rt.HoldCode != *hold {
				changes["hold_since"] = sql.NullTime{Time: now, Valid: true}
			}
		} else {
			changes["hold_since"] = sql.NullTime{}
		}
	}
	if progressed != nil && *progressed {
		changes["last_progress_at"] = sql.NullTime{Time: now, Valid: true}
	}
	// Best effort by design: a missed heartbeat is visible as STALLED, and must never fail the pass.
	_ = api.RDB.DB(ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", op.ID).Updates(changes).Error
}

// fleetAutoPause moves a RUNNING operation to PAUSED with a reason, through the same fence as any control
// transition. It is refused as stale (ILLEGAL_TRANSITION) when the operation is no longer in the epoch the
// decision was made in: a decision taken under an old state is never applied to a newer one.
func (api *Api) fleetAutoPause(ctx context.Context, token string, epoch int64, reason string) (*FleetOperation, error) {
	return api.fleetControl(ctx, token, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		if op.Dispatch != FleetDispatchRunning || op.ExecutionEpoch != epoch {
			return nil, nil, illegal(op, "paused by the driver")
		}
		return map[string]any{"dispatch": FleetDispatchPaused, "pause_reason": reason},
			&FleetOperationEvent{Kind: fleetEventAutoPause, Reason: reason}, nil
	})
}

func (api *Api) pauseDriven(ctx context.Context, res *FleetDriveResult, op *FleetOperation, reason string) (*FleetDriveResult, error) {
	if _, err := api.fleetAutoPause(ctx, op.Token, op.ExecutionEpoch, reason); err != nil && !isStaleDecision(err) {
		return nil, err
	}
	res.Paused = reason
	return res, nil
}

// finishDriven moves an operation whose lines have all been decided to DISPATCHED, under the fence, after
// re-checking under it that nothing is PLANNED (a retry bookkeeping or a cancel cannot have added one).
func (api *Api) finishDriven(ctx context.Context, res *FleetDriveResult, op *FleetOperation) (*FleetDriveResult, error) {
	_, err := api.fleetControl(ctx, op.Token, func(tx *gorm.DB, cur *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		if cur.Dispatch != FleetDispatchRunning || cur.ExecutionEpoch != op.ExecutionEpoch {
			return nil, nil, illegal(cur, "dispatched")
		}
		var planned int64
		if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", cur.ID, FleetPostingPlanned).Count(&planned).Error; err != nil {
			return nil, nil, err
		}
		if planned > 0 {
			return nil, nil, illegal(cur, "dispatched while lines remain")
		}
		var posted int64
		if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", cur.ID, FleetPostingPosted).Count(&posted).Error; err != nil {
			return nil, nil, err
		}
		now := time.Now()
		changes := map[string]any{"dispatch": FleetDispatchDispatched, "dispatched_at": sql.NullTime{Time: now, Valid: true}}
		if posted == 0 {
			changes["evidence"], changes["closed_at"] = fleetEvidenceClosedText, sql.NullTime{Time: now, Valid: true}
		}
		return changes, &FleetOperationEvent{Kind: fleetEventDispatched}, nil
	})
	if err != nil && !isStaleDecision(err) {
		return res, err
	}
	res.Dispatched = err == nil
	return res, nil
}

// fleetClock is the platform clock the driver reads for its windows and heartbeats; a variable so a test can move it.
var fleetClock = time.Now

// FleetDispatchDispatched is the state an operation rests in once every line has been decided.
const FleetDispatchDispatched = string(ledger.DispatchDispatched)
