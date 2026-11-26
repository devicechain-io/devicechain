// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var alice = FleetApprover{Name: "alice@example.com", Authorities: "device:write fleet:operate", Session: "sid-1"}

// armed lets the transitions run on a database that has no attribute guard to find.
func armed(t *testing.T) {
	t.Helper()
	openTheEngine(t)
	restore := OpenFleetActivationForTest(func(*gorm.DB) error { return nil })
	t.Cleanup(restore)
}

func readyOp(t *testing.T, api *Api, ctx context.Context, token string, safety FleetSafetyRequest, devices ...string) *FleetOperation {
	t.Helper()
	req := setReq(token, devices...)
	req.Safety = safety
	op, err := api.CreateFleetOperation(ctx, req, "creator")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, op.Dispatch)
	return op
}

func running(t *testing.T, api *Api, ctx context.Context, token string, safety FleetSafetyRequest) *FleetOperation {
	t.Helper()
	op := readyOp(t, api, ctx, token, safety, "d1", "d2")
	op, err := api.StartFleetOperation(ctx, token, op.ContractDigest, alice)
	require.NoError(t, err)
	return op
}

func events(t *testing.T, api *Api, ctx context.Context, op *FleetOperation) []string {
	t.Helper()
	var rows []FleetOperationEvent
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).Order("id").Find(&rows).Error)
	kinds := make([]string, len(rows))
	for i, r := range rows {
		kinds[i] = r.Kind
	}
	return kinds
}

func TestStartApprovesTheContractAndBindsTheGrant(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := readyOp(t, api, ctx, "s1", FleetSafetyRequest{}, "d1", "d2")
	started, err := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, started.Dispatch)
	require.Equal(t, alice.Name, started.Grantee)
	require.True(t, started.StartedAt.Valid)
	require.True(t, started.GrantBoundAt.Valid)
	require.Equal(t, op.ExecutionEpoch+1, started.ExecutionEpoch, "every control transition bumps the epoch")
	var contract FleetOperationContract
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&contract).Error)
	require.Equal(t, alice.Name, contract.ApprovedBy.String, "the starter approves; the creator only proposed")
	require.Equal(t, "creator", contract.ProposedBy)
	require.Equal(t, alice.Authorities, contract.Authorities.String)
	require.Equal(t, "sid-1", contract.ActorSession.String)
	require.True(t, contract.ApprovedAt.Valid)
	require.True(t, contract.ActivatedAt.Valid)
	require.Equal(t, []string{"CREATE", "START"}, events(t, api, ctx, op))
}

func TestStartRefusesWhatIsNotTheReviewedContract(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := readyOp(t, api, ctx, "s1", FleetSafetyRequest{}, "d1", "d2")
	_, err := api.StartFleetOperation(ctx, "s1", "sha256:"+fmt.Sprintf("%064d", 0), alice)
	requireFleetCode(t, err, FleetCodeContractDigestMismatch)
	_, err = api.StartFleetOperation(ctx, "s1", "", alice)
	requireFleetCode(t, err, FleetCodeContractDigestMismatch)
	again, err := api.fleetOperationByToken(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, again.Dispatch, "a refused start changes nothing")
	require.Equal(t, op.ExecutionEpoch, again.ExecutionEpoch)
	// Negative control: the right digest starts it.
	_, err = api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	require.NoError(t, err)
}

func TestStartAndTransitionsRefuseTheWrongState(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := running(t, api, ctx, "s1", FleetSafetyRequest{})
	_, err := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeIllegalTransition)
	_, err = api.ResumeFleetOperation(ctx, "s1", op.ContractDigest, nil, alice)
	requireFleetCode(t, err, FleetCodeIllegalTransition)
	_, err = api.PauseFleetOperation(ctx, "s1", alice.Name)
	require.NoError(t, err)
	_, err = api.PauseFleetOperation(ctx, "s1", alice.Name)
	requireFleetCode(t, err, FleetCodeIllegalTransition)
	_, err = api.AbortFleetOperation(ctx, "s1", alice.Name)
	require.NoError(t, err)
	for name, call := range map[string]func() error{
		"start":  func() error { _, e := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice); return e },
		"pause":  func() error { _, e := api.PauseFleetOperation(ctx, "s1", alice.Name); return e },
		"resume": func() error { _, e := api.ResumeFleetOperation(ctx, "s1", op.ContractDigest, nil, alice); return e },
		"abort":  func() error { _, e := api.AbortFleetOperation(ctx, "s1", alice.Name); return e },
	} {
		requireFleetCode(t, call(), FleetCodeIllegalTransition)
		_ = name
	}
	_, err = api.PauseFleetOperation(ctx, "nope", alice.Name)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestStartIsRefusedWhenTheDatabaseDoesNotEnforceTheProtocol(t *testing.T) {
	openTheEngine(t)
	restore := OpenFleetActivationForTest(func(*gorm.DB) error { return ErrFleetOperationNotActivated })
	defer restore()
	api, ctx := fleetFixture(t, 2)
	op := readyOp(t, api, ctx, "s1", FleetSafetyRequest{}, "d1")
	_, err := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeNotActivated)
	got, err := api.fleetOperationByToken(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, got.Dispatch)
}

func TestStartRefusesAnExpiredOperation(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := readyOp(t, api, ctx, "s1", FleetSafetyRequest{}, "d1")
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{ID: op.ID}).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	_, err := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeExpired)
}

func TestDispatchingBudgetBoundsStartedAndPausedOperations(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	for i := 0; i < ledger.TenantDispatchingOperations; i++ {
		running(t, api, ctx, fmt.Sprintf("r%d", i), FleetSafetyRequest{})
	}
	op := readyOp(t, api, ctx, "third", FleetSafetyRequest{}, "d1")
	_, err := api.StartFleetOperation(ctx, "third", op.ContractDigest, alice)
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe), "%v", err)
	require.Equal(t, FleetCodeQuotaExceeded, fe.Code)
	require.Equal(t, "tenant.dispatchingOperations", fe.Quota)
	// A paused operation still counts; an aborted one frees its slot.
	_, err = api.PauseFleetOperation(ctx, "r0", alice.Name)
	require.NoError(t, err)
	_, err = api.StartFleetOperation(ctx, "third", op.ContractDigest, alice)
	require.Error(t, err)
	_, err = api.AbortFleetOperation(ctx, "r0", alice.Name)
	require.NoError(t, err)
	_, err = api.StartFleetOperation(ctx, "third", op.ContractDigest, alice)
	require.NoError(t, err)
}

func TestAbortCancelsOnlyPlannedLinesAndClosesEvidenceOnlyWhenNothingWasApplied(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 3)
	// Aborting a READY operation: nothing was applied, so there is no evidence to wait for.
	ready := readyOp(t, api, ctx, "never", FleetSafetyRequest{}, "d1", "ghost", "d2")
	aborted, err := api.AbortFleetOperation(ctx, "never", alice.Name)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchAborted, aborted.Dispatch)
	require.Equal(t, "CLOSED", aborted.Evidence)
	require.True(t, aborted.ClosedAt.Valid)
	p, err := api.FleetOperationProgressOf(ctx, aborted)
	require.NoError(t, err)
	require.Equal(t, int32(2), p.Cancelled)
	require.Equal(t, int32(1), p.Refused, "a refused line is not rewritten")
	require.Zero(t, p.Planned)
	require.Equal(t, []string{"CREATE", "ABORT"}, events(t, api, ctx, ready))

	// An operation with an applied line keeps its evidence open and its applied line untouched.
	op := running(t, api, ctx, "partial", FleetSafetyRequest{})
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ? AND ordinal = 1", op.ID).
		Updates(map[string]any{"posting": FleetPostingPosted}).Error)
	got, err := api.AbortFleetOperation(ctx, "partial", alice.Name)
	require.NoError(t, err)
	require.Equal(t, "OPEN", got.Evidence)
	p, err = api.FleetOperationProgressOf(ctx, got)
	require.NoError(t, err)
	require.Equal(t, int32(1), p.Posted)
	require.Equal(t, int32(1), p.Cancelled)
}

func TestResumeKeepsToTheApprovedContract(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	rate, fail := int32(20), int32(5)
	op := running(t, api, ctx, "r", FleetSafetyRequest{MaxPostsPerSecond: &rate, FailureThreshold: &fail})
	paused, err := api.PauseFleetOperation(ctx, "r", alice.Name)
	require.NoError(t, err)
	require.Equal(t, FleetPauseUser, paused.PauseReason)

	// Wider than the contract: refused, and nothing changes.
	for name, wider := range map[string]*FleetResumeSafety{
		"rate above the ceiling":   {MaxPostsPerSecond: ptr32(21)},
		"threshold above":          {FailureThreshold: ptr32(6)},
		"threshold removed":        {ClearFailureThreshold: true},
		"ambiguity limit appeared": nil, // the contract has none: a limit is always within it
	} {
		if wider == nil {
			continue
		}
		_, err := api.ResumeFleetOperation(ctx, "r", op.ContractDigest, wider, alice)
		requireFleetCode(t, err, FleetCodeContractWidened)
		_ = name
		pv, err := api.PreviewFleetOperationResume(ctx, "r", wider)
		require.NoError(t, err)
		require.False(t, pv.WithinContract, name)
	}
	still, err := api.fleetOperationByToken(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchPaused, still.Dispatch)
	require.Equal(t, paused.ExecutionEpoch, still.ExecutionEpoch)

	// Wrong digest.
	_, err = api.ResumeFleetOperation(ctx, "r", "sha256:wrong", nil, alice)
	requireFleetCode(t, err, FleetCodeContractDigestMismatch)

	// Within the contract: lower the rate, add an ambiguity limit, tighten the failure limit.
	pv, err := api.PreviewFleetOperationResume(ctx, "r", &FleetResumeSafety{MaxPostsPerSecond: ptr32(5), FailureThreshold: ptr32(2), AmbiguityThreshold: ptr32(3)})
	require.NoError(t, err)
	require.True(t, pv.WithinContract)
	require.Equal(t, op.ContractDigest, pv.ContractDigest)
	require.False(t, pv.StepUpRequired)
	resumed, err := api.ResumeFleetOperation(ctx, "r", op.ContractDigest, &FleetResumeSafety{MaxPostsPerSecond: ptr32(5), FailureThreshold: ptr32(2), AmbiguityThreshold: ptr32(3)}, alice)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, resumed.Dispatch)
	require.Equal(t, int32(5), resumed.EffPostsPerSecond)
	require.Equal(t, int32(20), resumed.MaxPostsPerSecond, "the approved ceiling never moves")
	require.Equal(t, int32(2), resumed.EffFailureThreshold.Int32)
	require.Equal(t, int32(5), resumed.FailureThreshold.Int32)
	require.Equal(t, int32(3), resumed.EffAmbiguityThreshold.Int32)
	require.False(t, resumed.AmbiguityThreshold.Valid)
	require.Equal(t, []string{"CREATE", "START", "PAUSE", "RESUME"}, events(t, api, ctx, op))

	// Raising the rate back up to the ceiling is within the contract.
	_, err = api.PauseFleetOperation(ctx, "r", alice.Name)
	require.NoError(t, err)
	back, err := api.ResumeFleetOperation(ctx, "r", op.ContractDigest, &FleetResumeSafety{MaxPostsPerSecond: ptr32(20)}, alice)
	require.NoError(t, err)
	require.Equal(t, int32(20), back.EffPostsPerSecond)
	_, err = api.PauseFleetOperation(ctx, "r", alice.Name)
	require.NoError(t, err)
	for _, bad := range []*FleetResumeSafety{{MaxPostsPerSecond: ptr32(0)}, {FailureThreshold: ptr32(0)},
		{FailureThreshold: ptr32(1), ClearFailureThreshold: true}} {
		_, err := api.ResumeFleetOperation(ctx, "r", op.ContractDigest, bad, alice)
		requireFleetCode(t, err, FleetCodeInvalidRequest)
	}
}

func TestResumeRefusesWhileTheFailureLimitIsStillReached(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	fail := int32(2)
	op := running(t, api, ctx, "t", FleetSafetyRequest{FailureThreshold: &fail})
	_, err := api.PauseFleetOperation(ctx, "t", alice.Name)
	require.NoError(t, err)
	db := api.RDB.DB(ctx)
	// Two applied lines the device rejected, plus a plan-time refusal that must not count.
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).
		Updates(map[string]any{"posting": FleetPostingPosted, "settlement": string(ledger.SettlementFailed)}).Error)
	require.NoError(t, db.Create(&FleetOperationLine{OperationId: int64(op.ID), Ordinal: 9, Posting: FleetPostingRefused, Code: "TARGET_NOT_FOUND", RefusedAtPlan: true}).Error)
	pv, err := api.PreviewFleetOperationResume(ctx, "t", nil)
	require.NoError(t, err)
	require.True(t, pv.ThresholdStillExceeded)
	_, err = api.ResumeFleetOperation(ctx, "t", op.ContractDigest, nil, alice)
	requireFleetCode(t, err, FleetCodeThresholdStillExceeded)
	// Tightening it cannot help; the contract's own limit is the ceiling for a raise.
	_, err = api.ResumeFleetOperation(ctx, "t", op.ContractDigest, &FleetResumeSafety{FailureThreshold: ptr32(2)}, alice)
	requireFleetCode(t, err, FleetCodeThresholdStillExceeded)

	// Negative control: only the plan-time refusal on a fresh operation does not hold it back.
	fresh := running(t, api, ctx, "fresh", FleetSafetyRequest{FailureThreshold: &fail})
	_, err = api.PauseFleetOperation(ctx, "fresh", alice.Name)
	require.NoError(t, err)
	require.NoError(t, db.Create(&FleetOperationLine{OperationId: int64(fresh.ID), Ordinal: 9, Posting: FleetPostingRefused, Code: "TARGET_NOT_FOUND", RefusedAtPlan: true}).Error)
	require.NoError(t, db.Create(&FleetOperationLine{OperationId: int64(fresh.ID), Ordinal: 10, Posting: FleetPostingRefused, Code: "TARGET_NOT_FOUND", RefusedAtPlan: true}).Error)
	_, err = api.ResumeFleetOperation(ctx, "fresh", fresh.ContractDigest, nil, alice)
	require.NoError(t, err)
}

func TestResumeByAnotherPersonIsAHandover(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := running(t, api, ctx, "h", FleetSafetyRequest{})
	_, err := api.PauseFleetOperation(ctx, "h", "someone-else@example.com")
	require.NoError(t, err)
	bob := FleetApprover{Name: "bob@example.com", Authorities: alice.Authorities}
	resumed, err := api.ResumeFleetOperation(ctx, "h", op.ContractDigest, nil, bob)
	require.NoError(t, err)
	require.Equal(t, bob.Name, resumed.Grantee)
	var ev FleetOperationEvent
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ? AND kind = ?", op.ID, "HANDOVER").First(&ev).Error)
	require.Equal(t, alice.Name, ev.FromGrantee.String)
	require.Equal(t, bob.Name, ev.ToGrantee.String)
	// The grantee resuming their own pause is a plain RESUME.
	_, err = api.PauseFleetOperation(ctx, "h", bob.Name)
	require.NoError(t, err)
	_, err = api.ResumeFleetOperation(ctx, "h", op.ContractDigest, nil, bob)
	require.NoError(t, err)
	require.Equal(t, []string{"CREATE", "START", "PAUSE", "HANDOVER", "PAUSE", "RESUME"}, events(t, api, ctx, op))
}

func TestExpiryAbortsTheOperationsNobodyFinishedAndNoOthers(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	now := time.Now()
	expired := readyOp(t, api, ctx, "expired", FleetSafetyRequest{}, "d1")
	fresh := readyOp(t, api, ctx, "fresh", FleetSafetyRequest{}, "d1")
	started := running(t, api, ctx, "started", FleetSafetyRequest{})
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(&FleetOperation{ID: expired.ID}).Update("expires_at", now.Add(-time.Hour)).Error)
	// A started operation keeps its (now past) READY expiry stamp; it must not be touched.
	require.NoError(t, db.Model(&FleetOperation{ID: started.ID}).Update("expires_at", now.Add(-time.Hour)).Error)
	// A header stuck in PLANNING for too long, and one that is recent.
	n, err := normalizeFleetRequest(setReq("stuck", "d1"))
	require.NoError(t, err)
	plan, err := api.planNormalized(ctx, n)
	require.NoError(t, err)
	stuck, err := api.fleetCreateStepOne(ctx, "acme", setReq("stuck", "d1"), n, plan, "creator")
	require.NoError(t, err)
	n2, err := normalizeFleetRequest(setReq("recent", "d1"))
	require.NoError(t, err)
	recent, err := api.fleetCreateStepOne(ctx, "acme", setReq("recent", "d1"), n2, plan, "creator")
	require.NoError(t, err)
	require.NoError(t, db.Model(&FleetOperation{ID: stuck.ID}).Update("created_at", now.Add(-time.Hour)).Error)

	count, err := api.ExpireFleetOperations(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	get := func(token string) *FleetOperation {
		op, err := api.fleetOperationByToken(ctx, token)
		require.NoError(t, err)
		return op
	}
	require.Equal(t, FleetDispatchAborted, get("expired").Dispatch)
	require.Equal(t, FleetAbortExpiredUnstarted, get("expired").PauseReason)
	require.Equal(t, FleetDispatchAborted, get("stuck").Dispatch)
	require.Equal(t, FleetAbortPlanningDropped, get("stuck").PauseReason)
	require.Equal(t, FleetDispatchReady, get("fresh").Dispatch)
	require.Equal(t, FleetDispatchRunning, get("started").Dispatch)
	require.Equal(t, FleetDispatchPlanning, get("recent").Dispatch)
	require.Equal(t, []string{"CREATE", "EXPIRE"}, events(t, api, ctx, expired))
	_ = fresh
	_ = recent
	// Idempotent: nothing more is due.
	count, err = api.ExpireFleetOperations(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	// Another tenant's operations are never swept from this tenant's context.
	other := core.WithTenant(context.Background(), "globex")
	count, err = api.ExpireFleetOperations(other)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestResumeIsRefusedWhenTheGuardIsNoLongerInPlace(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := running(t, api, ctx, "g", FleetSafetyRequest{})
	_, err := api.PauseFleetOperation(ctx, "g", alice.Name)
	require.NoError(t, err)
	restore := OpenFleetActivationForTest(func(*gorm.DB) error { return ErrFleetOperationNotActivated })
	_, err = api.ResumeFleetOperation(ctx, "g", op.ContractDigest, nil, alice)
	restore()
	requireFleetCode(t, err, FleetCodeNotActivated)
	got, err := api.fleetOperationByToken(ctx, "g")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchPaused, got.Dispatch, "a refused resume changes nothing")
	// Negative control: with the guard back, the same resume lands.
	_, err = api.ResumeFleetOperation(ctx, "g", op.ContractDigest, nil, alice)
	require.NoError(t, err)
}
