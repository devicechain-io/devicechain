// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The control fence. Every control transition (start, pause, resume, abort, expire) runs the same
// protocol, and it is the ONLY way the dispatch state of an operation changes:
//
//	(b) BEGIN; take the operation's fence (K_op) EXCLUSIVE, waiting at most 15 s;
//	(c) read the operation row FOR UPDATE and check the transition is legal;
//	(d) apply it, bump the execution epoch, append the event;
//	(e) COMMIT.
//
// Every posting transaction takes the same fence SHARED and re-reads dispatch and epoch after it holds
// it, so once a control call returns, no line is applied under the old state. A wait that times out
// answers FENCE_PENDING and has changed nothing. The posting path (a later change) never touches the
// operation row; the lock order is: fence, then operation row, then (start and resume only) the
// admission locks.

// Control refusals. They ride extensions.code.
const (
	FleetCodeFencePending           = "FENCE_PENDING"
	FleetCodeIllegalTransition      = "ILLEGAL_TRANSITION"
	FleetCodeContractDigestMismatch = "CONTRACT_DIGEST_MISMATCH"
	FleetCodeContractWidened        = "CONTRACT_WIDENED"
	FleetCodeThresholdStillExceeded = "THRESHOLD_STILL_EXCEEDED"
	// FleetCodeNotActivated is answered when the database does not enforce the device attribute
	// writer protocol or the caller's tenant still has orphaned attribute rows.
	FleetCodeNotActivated = "BULK_CHANGES_NOT_ACTIVATED"
	FleetCodeExpired      = "OPERATION_EXPIRED"

	// FleetCodeNewerThanServer refuses control that requires understanding the operation's protocol.
	FleetCodeNewerThanServer = "OPERATION_NEWER_THAN_SERVER"
)

// Pause reasons this release writes.
const (
	FleetPauseUser             = "USER"
	FleetAbortExpiredUnstarted = "EXPIRED_UNSTARTED"
	FleetAbortPlanningDropped  = "PLANNING_ABANDONED"
)

// Dispatch states and event kinds the control path writes.
const (
	FleetDispatchRunning    = string(ledger.DispatchRunning)
	FleetDispatchPaused     = string(ledger.DispatchPaused)
	FleetDispatchAborted    = string(ledger.DispatchAborted)
	FleetPostingCancelled   = string(ledger.PostingCancelled)
	FleetPostingPosted      = string(ledger.PostingPosted)
	fleetEventStart         = "START"
	fleetEventPause         = "PAUSE"
	fleetEventResume        = "RESUME"
	fleetEventAbort         = "ABORT"
	fleetEventHandover      = "HANDOVER"
	fleetEventExpire        = "EXPIRE"
	fleetStartAuthorities   = "device:write fleet:operate"
	fleetEvidenceClosedText = "CLOSED"
)

// fleetFenceWait bounds how long a control transition waits for the posters in flight. Each poster
// lives at most 8 s, so the wait has real margin. It is a variable only so a test can shorten it.
var fleetFenceWait = "15s"

// fleetActivationCheck is the posting-time check of the database guard; a variable only so a test of the
// transitions on a database that has no such guard (the SQLite fixtures) can stand in for it.
var fleetActivationCheck = CheckFleetOperationActivation

// Fixed release budgets; tests may lower them to exercise upgrades and quota boundaries
// without inserting a million lines. These are not operator configuration.
var fleetAdmissionBudgets = struct {
	tenantDispatching, instanceDispatching, tenantEvidenceOpen, instanceEvidenceOpen int64
}{ledger.TenantDispatchingOperations, ledger.InstanceDispatchingOperations,
	ledger.TenantEvidenceOpenLines, ledger.InstanceEvidenceOpenLines}

// The bounded orphan scan is an activation gate, never part of a fenced transaction.
// A previous-release delete can leave these rows behind during a rolling upgrade;
// they cannot affect a live device's before-image, so posting does not repeat this scan.
func (api *Api) fleetCheckOrphans(ctx context.Context) error {
	if tenant, ok := core.TenantFromContext(ctx); !ok || tenant == "" {
		return errors.New("a bulk change needs a tenant in the context")
	}
	orphans, err := api.CountOrphanedDeviceAttributes(ctx)
	if err != nil || orphans > 0 {
		return fleetErr(FleetCodeNotActivated, "orphaned device attribute rows remain; the sweep removes them")
	}
	return nil
}

// OpenFleetActivationForTest replaces the activation check and returns the function that restores it.
func OpenFleetActivationForTest(check func(tx *gorm.DB) error) (restore func()) {
	prev := fleetActivationCheck
	fleetActivationCheck = check
	return func() { fleetActivationCheck = prev }
}

// ShortenFleetFenceWaitForTest sets the fence wait and returns the function that restores it.
func ShortenFleetFenceWaitForTest(wait string) (restore func()) {
	prev := fleetFenceWait
	fleetFenceWait = wait
	return func() { fleetFenceWait = prev }
}

// FleetApprover is who approves a contract at start: the sign-in name, the authorities that were
// checked, and the token's session identifier when the claims carry one.
type FleetApprover struct {
	Name        string
	Authorities string
	Session     string
}

// FleetResumeSafety is an optional change to the effective limits at resume. A nil field keeps the
// current value; the Clear flags remove a threshold. Anything looser than the approved contract is
// refused (CONTRACT_WIDENED): a wider contract is a new approval, which this release does not offer.
type FleetResumeSafety struct {
	MaxPostsPerSecond       *int32
	FailureThreshold        *int32
	AmbiguityThreshold      *int32
	ClearFailureThreshold   bool
	ClearAmbiguityThreshold bool
}

// fleetControl runs one control transition under the fence. fn runs with the operation row locked and
// the transition not yet applied; it returns the changes and the event to append.
// Only an explicit pause may touch a newer protocol: it changes no lines.
func (api *Api) fleetControl(ctx context.Context, token, transition string, fn func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error)) (*FleetOperation, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("a bulk change needs a tenant in the context")
	}
	head, err := api.fleetOperationByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, gorm.ErrRecordNotFound
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fleetSetLockTimeout(tx, fleetFenceWait); err != nil {
			return err
		}
		if err := fleetLock(tx, fleetOpKey(tenant, head.ID), false); err != nil {
			return err
		}
		var op FleetOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, head.ID).Error; err != nil {
			return err
		}
		if transition != fleetEventPause && !ledger.Drivable(ledger.DriverProtocol(op.DriverProtocol)) {
			return fleetErr(FleetCodeNewerThanServer, "this bulk change was created by a newer release; retry, or pause it")
		}
		changes, event, err := fn(tx, &op)
		if err != nil {
			return err
		}
		now := time.Now()
		changes["execution_epoch"] = op.ExecutionEpoch + 1
		changes["state_changed_at"] = now
		if err := tx.Model(&FleetOperation{ID: op.ID}).Updates(changes).Error; err != nil {
			return err
		}
		if event != nil {
			event.OperationId = int64(op.ID)
			event.At = now
			event.Epoch = sql.NullInt64{Int64: op.ExecutionEpoch + 1, Valid: true}
			if err := tx.Create(event).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if lockTimedOut(err) {
			// Nothing has changed: the caller may retry, and may pause first to stop further posting.
			return nil, fleetErr(FleetCodeFencePending, "the operation is still draining in-flight work; retry")
		}
		return nil, err
	}
	return api.fleetOperationByToken(ctx, token)
}

func illegal(op *FleetOperation, to string) error {
	return fleetErr(FleetCodeIllegalTransition, "an operation that is %s cannot be %s", op.Dispatch, to)
}

func (api *Api) fleetBrokerFailClosed() bool {
	return api.FleetNatsConfig != nil && api.FleetNatsConfig.MqttFailClosed
}

// StartFleetOperation moves a READY operation to RUNNING, on the strength of the contract digest the
// caller presents: what the person reviewed is what runs. The starter approves contract version 1 and
// becomes the grantee the engine runs on behalf of. Broker fail-closed is snapshotted as informational
// evidence only; it does not affect admission, dispatch or settlement.
func (api *Api) StartFleetOperation(ctx context.Context, token, contractDigest string, by FleetApprover) (*FleetOperation, error) {
	if err := api.fleetCheckOrphans(ctx); err != nil {
		return nil, err
	}
	tenant, _ := core.TenantFromContext(ctx)
	return api.fleetControl(ctx, token, fleetEventStart, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		if op.Dispatch != FleetDispatchReady {
			return nil, nil, illegal(op, "started")
		}
		if contractDigest != op.ContractDigest {
			return nil, nil, fleetErr(FleetCodeContractDigestMismatch, "the contract digest presented is not the operation's current one")
		}
		if op.ExpiresAt.Valid && !op.ExpiresAt.Time.After(time.Now()) {
			return nil, nil, fleetErr(FleetCodeExpired, "the operation expired before it was started")
		}
		if err := fleetActivationCheck(tx); err != nil {
			if errors.Is(err, ErrFleetOperationNotActivated) {
				return nil, nil, fleetErr(FleetCodeNotActivated, "%s", err.Error())
			}
			return nil, nil, err
		}
		if err := api.fleetAdmitDispatching(ctx, tx, tenant, op); err != nil {
			return nil, nil, err
		}
		now := time.Now()
		if err := api.fleetApproveContract(tx, op, by, now); err != nil {
			return nil, nil, err
		}
		return map[string]any{
			"dispatch": FleetDispatchRunning, "pause_reason": "", "grantee": by.Name,
			"grant_bound_at": sql.NullTime{Time: now, Valid: true}, "started_at": sql.NullTime{Time: now, Valid: true},
			"state_changed_by": by.Name, "broker_fail_closed": api.fleetBrokerFailClosed(),
		}, &FleetOperationEvent{Kind: fleetEventStart, Actor: sql.NullString{String: by.Name, Valid: true},
			ContractVersion: sql.NullInt32{Int32: op.ContractVersion, Valid: true}}, nil
	})
}

func (api *Api) fleetApproveContract(tx *gorm.DB, op *FleetOperation, by FleetApprover, now time.Time) error {
	updates := map[string]any{
		"approved_by": sql.NullString{String: by.Name, Valid: true}, "authorities": sql.NullString{String: by.Authorities, Valid: true},
		"approved_at": sql.NullTime{Time: now, Valid: true}, "activated_at": sql.NullTime{Time: now, Valid: true},
	}
	if by.Session != "" {
		updates["actor_session"] = sql.NullString{String: by.Session, Valid: true}
	}
	res := tx.Model(&FleetOperationContract{}).Where("operation_id = ? AND version = ?", op.ID, op.ContractVersion).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("the operation's contract version is missing")
	}
	return nil
}

// fleetAdmitDispatching recounts dispatch slots and evidence exposure under K_adm(tenant)
// then K_adm(instance), on the transaction holding both locks until the transition commits.
// Excluding the candidate before adding one makes start and resume share the same rule,
// including when a later release lowers a budget below the already-admitted population.
func (api *Api) fleetAdmitDispatching(ctx context.Context, tx *gorm.DB, tenant string, op *FleetOperation) error {
	if err := fleetSetLockTimeout(tx, fleetAdmissionLockTimeout); err != nil {
		return err
	}
	for _, key := range []int64{fleetAdmissionTenantKey(tenant), fleetAdmissionInstanceKey()} {
		if err := fleetLock(tx, key, false); err != nil {
			if lockTimedOut(err) {
				return fleetErr(FleetCodeAdmissionBusy, "bulk change admission is busy; retry")
			}
			return err
		}
	}
	// The fence wait set above is restored for the rest of the transaction.
	if err := fleetSetLockTimeout(tx, fleetFenceWait); err != nil {
		return err
	}
	dispatching := []string{FleetDispatchRunning, FleetDispatchPaused}
	var tenantCount int64
	if err := tx.Model(&FleetOperation{}).Where("dispatch IN ? AND id <> ?", dispatching, op.ID).Count(&tenantCount).Error; err != nil {
		return err
	}
	if tenantCount+1 > fleetAdmissionBudgets.tenantDispatching {
		e := fleetErr(FleetCodeQuotaExceeded, "the tenant already has %d operations running or paused (limit %d)", tenantCount, fleetAdmissionBudgets.tenantDispatching)
		e.Quota = "tenant.dispatchingOperations"
		return e
	}
	var instanceCount int64
	// System context reads counts only for the instance budgets, on this same locked transaction.
	systemTx := tx.WithContext(core.WithSystemContext(ctx))
	if err := systemTx.Model(&FleetOperation{}).Where("dispatch IN ? AND id <> ?", dispatching, op.ID).Count(&instanceCount).Error; err != nil {
		return err
	}
	if instanceCount+1 > fleetAdmissionBudgets.instanceDispatching {
		e := fleetErr(FleetCodeQuotaExceeded, "the instance-wide limit dispatchingOperations is reached; retry later")
		e.Quota = "instance.dispatchingOperations"
		return e
	}
	tenantOpen, err := fleetEvidenceOpenCount(tx, op)
	if err != nil {
		return err
	}
	if tenantOpen > fleetAdmissionBudgets.tenantEvidenceOpen {
		e := fleetErr(FleetCodeQuotaExceeded, "the tenant evidence-open line limit is reached; retry after evidence settles")
		e.Quota = "tenant.evidenceOpenLines"
		return e
	}
	instanceOpen, err := fleetEvidenceOpenCount(systemTx, op)
	if err != nil {
		return err
	}
	if instanceOpen > fleetAdmissionBudgets.instanceEvidenceOpen {
		e := fleetErr(FleetCodeQuotaExceeded, "the instance-wide evidence-open line limit is reached; retry after evidence settles")
		e.Quota = "instance.evidenceOpenLines"
		return e
	}
	return nil
}

// Count line state, irrespective of the header's dispatch/evidence: aborted operations
// can still be awaiting evidence. Only the candidate's remaining plans add new exposure.
func fleetEvidenceOpenCount(tx *gorm.DB, op *FleetOperation) (int64, error) {
	var n int64
	err := tx.Model(&FleetOperationLine{}).Where(`((posting = ? AND (settlement IS NULL OR settlement = ?) AND code <> ?)
		OR (operation_id = ? AND posting = ?))`, FleetPostingPosted, string(ledger.SettlementAmbiguous), "TARGET_ERASED",
		op.ID, FleetPostingPlanned).Count(&n).Error
	return n, err
}

// PauseFleetOperation moves a RUNNING operation to PAUSED. Once it returns, no line of the operation
// is applied or refused under the old state. Stopping never widens anything, so it needs no more
// authority than writing a device.
func (api *Api) PauseFleetOperation(ctx context.Context, token, actor string) (*FleetOperation, error) {
	return api.fleetControl(ctx, token, fleetEventPause, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		if op.Dispatch != FleetDispatchRunning {
			return nil, nil, illegal(op, "paused")
		}
		return map[string]any{"dispatch": FleetDispatchPaused, "pause_reason": FleetPauseUser, "state_changed_by": actor},
			&FleetOperationEvent{Kind: fleetEventPause, Reason: FleetPauseUser, Actor: sql.NullString{String: actor, Valid: true}}, nil
	})
}

// AbortFleetOperation stops an operation for good. Lines not yet applied are cancelled; applied lines
// keep settling. An aborted operation can still be reversed.
func (api *Api) AbortFleetOperation(ctx context.Context, token, actor string) (*FleetOperation, error) {
	return api.fleetControl(ctx, token, fleetEventAbort, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		switch op.Dispatch {
		case FleetDispatchReady, FleetDispatchRunning, FleetDispatchPaused:
		default:
			return nil, nil, illegal(op, "aborted")
		}
		return fleetAbortChanges(tx, op, actor, "", fleetEventAbort)
	})
}

// fleetAbortChanges cancels the PLANNED lines and returns the header changes of an abort. An
// operation that ends with nothing applied holds no evidence, so it is closed at once.
func fleetAbortChanges(tx *gorm.DB, op *FleetOperation, actor, reason, kind string) (map[string]any, *FleetOperationEvent, error) {
	if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPlanned).
		Update("posting", FleetPostingCancelled).Error; err != nil {
		return nil, nil, err
	}
	var posted int64
	if err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPosted).Count(&posted).Error; err != nil {
		return nil, nil, err
	}
	now := time.Now()
	changes := map[string]any{"dispatch": FleetDispatchAborted, "pause_reason": reason, "state_changed_by": actor,
		"dispatched_at": sql.NullTime{Time: now, Valid: true}}
	if posted == 0 {
		changes["evidence"] = fleetEvidenceClosedText
		changes["closed_at"] = sql.NullTime{Time: now, Valid: true}
	}
	ev := &FleetOperationEvent{Kind: kind, Reason: reason}
	if actor != "" {
		ev.Actor = sql.NullString{String: actor, Valid: true}
	}
	return changes, ev, nil
}

// FleetResumePreview is what resuming with a given safety would do, computed without writing.
type FleetResumePreview struct {
	WithinContract         bool
	ContractVersion        int32
	ContractDigest         string
	Contract               string
	StepUpRequired         bool
	ThresholdStillExceeded bool
}

func fleetContractSafety(op *FleetOperation) ledger.Safety {
	s := ledger.Safety{SettleDeadlineSeconds: int64(op.SettleDeadlineS)}
	rate := int64(op.MaxPostsPerSecond)
	s.MaxPostsPerSecond = &rate
	if op.FailureThreshold.Valid {
		v := int64(op.FailureThreshold.Int32)
		s.FailureThreshold = &v
	}
	if op.AmbiguityThreshold.Valid {
		v := int64(op.AmbiguityThreshold.Int32)
		s.AmbiguityThreshold = &v
	}
	return s
}

// effectiveSafety applies a resume's requested change to the operation's current effective limits.
func effectiveSafety(op *FleetOperation, in *FleetResumeSafety) (rate int32, failure, ambiguity sql.NullInt32, err error) {
	rate, failure, ambiguity = op.EffPostsPerSecond, op.EffFailureThreshold, op.EffAmbiguityThreshold
	if in == nil {
		return
	}
	if in.MaxPostsPerSecond != nil {
		if *in.MaxPostsPerSecond < 1 {
			return 0, failure, ambiguity, fleetErr(FleetCodeInvalidRequest, "maxPostsPerSecond must be at least 1")
		}
		rate = *in.MaxPostsPerSecond
	}
	set := func(v *int32, clear bool, cur sql.NullInt32, name string) (sql.NullInt32, error) {
		if clear && v != nil {
			return cur, fleetErr(FleetCodeInvalidRequest, "%s cannot be both set and cleared", name)
		}
		if clear {
			return sql.NullInt32{}, nil
		}
		if v != nil {
			if *v < 1 {
				return cur, fleetErr(FleetCodeInvalidRequest, "%s must be at least 1", name)
			}
			return sql.NullInt32{Int32: *v, Valid: true}, nil
		}
		return cur, nil
	}
	if failure, err = set(in.FailureThreshold, in.ClearFailureThreshold, failure, "failureThreshold"); err != nil {
		return
	}
	ambiguity, err = set(in.AmbiguityThreshold, in.ClearAmbiguityThreshold, ambiguity, "ambiguityThreshold")
	return
}

// withinContract reports whether effective limits are no looser than the approved contract's: a rate
// at or below the ceiling (raising it back up to the ceiling is allowed), and a threshold at or below
// the contract's, or any threshold where the contract has none.
func withinContract(op *FleetOperation, rate int32, failure, ambiguity sql.NullInt32) bool {
	if rate > op.MaxPostsPerSecond {
		return false
	}
	loose := func(eff, contract sql.NullInt32) bool {
		if !contract.Valid {
			return false // the contract has none: anything is within it
		}
		return !eff.Valid || eff.Int32 > contract.Int32
	}
	return !loose(failure, op.FailureThreshold) && !loose(ambiguity, op.AmbiguityThreshold)
}

// fleetFailureCount counts lines that failed after being applied, plus those refused at post time.
// Plan-time refusals never count: an operation approved with five unknown tokens and a threshold of
// five does not pause at start.
func fleetFailureCount(tx *gorm.DB, op *FleetOperation) (int64, error) {
	var n int64
	err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND ((posting = ? AND settlement = ?) OR (posting = ? AND refused_at_plan = ?))",
		op.ID, FleetPostingPosted, string(ledger.SettlementFailed), FleetPostingRefused, false).Count(&n).Error
	return n, err
}

func fleetAmbiguityCount(tx *gorm.DB, op *FleetOperation) (int64, error) {
	var n int64
	err := tx.Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ? AND settlement = ? AND code <> ?",
		op.ID, FleetPostingPosted, string(ledger.SettlementAmbiguous), "TARGET_ERASED").Count(&n).Error
	return n, err
}

// PreviewFleetOperationResume reports what resuming with this safety would do. It writes nothing.
func (api *Api) PreviewFleetOperationResume(ctx context.Context, token string, in *FleetResumeSafety) (*FleetResumePreview, error) {
	op, err := api.fleetOperationByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, gorm.ErrRecordNotFound
	}
	rate, failure, ambiguity, err := effectiveSafety(op, in)
	if err != nil {
		return nil, err
	}
	body, err := api.FleetOperationContractBody(ctx, op)
	if err != nil {
		return nil, err
	}
	db := api.RDB.DB(ctx)
	failures, err := fleetFailureCount(db, op)
	if err != nil {
		return nil, err
	}
	ambiguities, err := fleetAmbiguityCount(db, op)
	if err != nil {
		return nil, err
	}
	return &FleetResumePreview{
		WithinContract: withinContract(op, rate, failure, ambiguity), ContractVersion: op.ContractVersion, ContractDigest: op.ContractDigest,
		Contract: body, StepUpRequired: false,
		ThresholdStillExceeded: (failure.Valid && failures >= int64(failure.Int32)) || (ambiguity.Valid && ambiguities >= int64(ambiguity.Int32)),
	}, nil
}

// ResumeFleetOperation moves a PAUSED operation back to RUNNING. The caller presents the current
// contract digest; a change of the effective limits is applied only if it stays within the approved
// contract, because widening a contract is a new approval this release does not offer. A resumer who is
// not the grantee must hold the authorities (the resolver checks them) and becomes the grantee.
func (api *Api) ResumeFleetOperation(ctx context.Context, token, contractDigest string, in *FleetResumeSafety, by FleetApprover) (*FleetOperation, error) {
	if err := api.fleetCheckOrphans(ctx); err != nil {
		return nil, err
	}
	tenant, _ := core.TenantFromContext(ctx)
	return api.fleetControl(ctx, token, fleetEventResume, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		if op.Dispatch != FleetDispatchPaused {
			return nil, nil, illegal(op, "resumed")
		}
		if contractDigest != op.ContractDigest {
			return nil, nil, fleetErr(FleetCodeContractDigestMismatch, "the contract digest presented is not the operation's current one")
		}
		rate, failure, ambiguity, err := effectiveSafety(op, in)
		if err != nil {
			return nil, nil, err
		}
		if !withinContract(op, rate, failure, ambiguity) {
			return nil, nil, fleetErr(FleetCodeContractWidened, "that change is wider than the approved contract, and approving a wider contract is not available in this release")
		}
		if err := fleetActivationCheck(tx); err != nil {
			if errors.Is(err, ErrFleetOperationNotActivated) {
				return nil, nil, fleetErr(FleetCodeNotActivated, "%s", err.Error())
			}
			return nil, nil, err
		}
		failures, err := fleetFailureCount(tx, op)
		if err != nil {
			return nil, nil, err
		}
		ambiguities, err := fleetAmbiguityCount(tx, op)
		if err != nil {
			return nil, nil, err
		}
		if (failure.Valid && failures >= int64(failure.Int32)) || (ambiguity.Valid && ambiguities >= int64(ambiguity.Int32)) {
			return nil, nil, fleetErr(FleetCodeThresholdStillExceeded, "the failure or ambiguity limit is still reached; raise it within the contract or abort")
		}
		if err := api.fleetAdmitDispatching(ctx, tx, tenant, op); err != nil {
			return nil, nil, err
		}
		changes := map[string]any{"dispatch": FleetDispatchRunning, "pause_reason": "", "eff_posts_per_second": rate,
			"eff_failure_threshold": failure, "eff_ambiguity_threshold": ambiguity, "state_changed_by": by.Name}
		// Re-read the instance setting on every resume, including a handover. Evidence can only weaken.
		changes["broker_fail_closed"] = op.BrokerFailClosed && api.fleetBrokerFailClosed()
		event := &FleetOperationEvent{Kind: fleetEventResume, Actor: sql.NullString{String: by.Name, Valid: true},
			ContractVersion: sql.NullInt32{Int32: op.ContractVersion, Valid: true}}
		if op.Grantee != by.Name {
			// Handover: the resumer holds the authorities and becomes the person the engine runs for.
			changes["grantee"] = by.Name
			changes["grant_bound_at"] = sql.NullTime{Time: time.Now(), Valid: true}
			event.Kind = fleetEventHandover
			event.FromGrantee = sql.NullString{String: op.Grantee, Valid: op.Grantee != ""}
			event.ToGrantee = sql.NullString{String: by.Name, Valid: true}
		}
		return changes, event, nil
	})
}

// ExpireFleetOperations aborts, for the tenant of ctx, the operations nobody finished: a READY one past
// its expiry (EXPIRED_UNSTARTED) and a header that has sat in PLANNING too long (PLANNING_ABANDONED),
// each through the same fence as any other control transition. It returns how many it aborted.
func (api *Api) ExpireFleetOperations(ctx context.Context, now time.Time) (int, error) {
	var due []FleetOperation
	if err := fleetExpiryCandidates(api.RDB.DB(ctx), now).Order("id").Limit(100).Find(&due).Error; err != nil {
		return 0, err
	}
	aborted := 0
	var lastErr error
	for _, d := range due {
		_, err := api.fleetControl(ctx, d.Token, fleetEventExpire, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
			// Re-judged under the fence: a start or a finisher may have got here first.
			stillReady := op.Dispatch == FleetDispatchReady && op.ExpiresAt.Valid && !op.ExpiresAt.Time.After(now)
			stillPlanning := op.Dispatch == FleetDispatchPlanning && !op.CreatedAt.After(now.Add(-ledger.PlanningAbandonedAfter))
			if !stillReady && !stillPlanning {
				return nil, nil, errFleetNotDue
			}
			reason := FleetAbortExpiredUnstarted
			if stillPlanning {
				reason = FleetAbortPlanningDropped
			}
			return fleetAbortChanges(tx, op, "", reason, fleetEventExpire)
		})
		if errors.Is(err, errFleetNotDue) {
			continue
		}
		var fe *FleetOperationError
		if errors.As(err, &fe) && (fe.Code == FleetCodeFencePending || fe.Code == FleetCodeNewerThanServer) {
			continue // a busy operation or a protocol changed after discovery must not starve the sweep
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return aborted, ctxErr
			}
			// One operation that keeps failing must not hold back the tenant's others, which sort after it.
			lastErr = err
			continue
		}
		aborted++
	}
	return aborted, lastErr
}

var errFleetNotDue = errors.New("the operation is no longer due")
