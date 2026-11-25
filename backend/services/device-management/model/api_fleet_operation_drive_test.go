// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeStanding struct {
	answer map[string]bool
	err    error
	asked  int
}

func (f *fakeStanding) PrincipalStanding(_ context.Context, principals, authorities []string) (map[string]bool, error) {
	f.asked++
	if f.err != nil {
		return nil, f.err
	}
	if len(authorities) != 2 {
		return nil, errors.New("the lease must ask for both authorities")
	}
	return f.answer, nil
}

type driveEnv struct {
	*postEnv
	standing *fakeStanding
	devs     []*Device
	now      time.Time
}

// driveFixture is n devices that declare configuration key k, and a RUNNING operation over all of them.
func driveFixture(t *testing.T, n int, safety FleetSafetyRequest) *driveEnv {
	t.Helper()
	armed(t)
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{},
		&FleetOperationLine{}, &FleetOperationEvent{}))
	d0 := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	devs := []*Device{d0}
	tokens := []string{d0.Token}
	for i := 1; i < n; i++ {
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("dev%d", i), DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		devs, tokens = append(devs, d), append(tokens, d.Token)
	}
	v := "v1"
	req := &FleetOperationCreateRequest{Token: "drv", Name: "n", Kind: FleetKindConfigSet, DeviceTokens: tokens, Safety: safety,
		ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v}}
	op, err := api.CreateFleetOperation(ctx, req, "creator")
	require.NoError(t, err)
	op, err = api.StartFleetOperation(ctx, "drv", op.ContractDigest, alice)
	require.NoError(t, err)
	standing := &fakeStanding{answer: map[string]bool{alice.Name: true}}
	api.PrincipalStanding = standing
	e := &driveEnv{postEnv: &postEnv{api: api, ctx: ctx, op: op}, standing: standing, devs: devs, now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	prev := fleetClock
	fleetClock = func() time.Time { return e.now }
	t.Cleanup(func() { fleetClock = prev })
	return e
}

func (e *driveEnv) drive(t *testing.T) *FleetDriveResult {
	t.Helper()
	r, err := e.api.DriveFleetOperation(e.ctx, "drv", DefaultFleetCaps)
	require.NoError(t, err)
	return r
}

func (e *driveEnv) current(t *testing.T) *FleetOperation {
	t.Helper()
	op, err := e.api.fleetOperationByToken(e.ctx, "drv")
	require.NoError(t, err)
	return op
}

func (e *driveEnv) count(t *testing.T, posting string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("operation_id = ? AND posting = ?", e.op.ID, posting).Count(&n).Error)
	return n
}

func TestAPassPostsEveryDueLineAndFinishesTheOperation(t *testing.T) {
	e := driveFixture(t, 3, FleetSafetyRequest{})
	r := e.drive(t)
	require.Equal(t, 3, r.Posted)
	require.True(t, r.Dispatched, "nothing left PLANNED means the operation's posting is finished")
	op := e.current(t)
	require.Equal(t, FleetDispatchDispatched, op.Dispatch)
	require.True(t, op.DispatchedAt.Valid)
	require.Equal(t, "OPEN", op.Evidence, "evidence is outstanding until the devices answer")
	require.Equal(t, int64(3), e.count(t, FleetPostingPosted))
	require.Equal(t, 1, e.standing.asked, "one fresh standing question per pass, never cached")
	// A pass over a finished operation is a no-op and asks nothing.
	r = e.drive(t)
	require.Zero(t, r.Posted)
	require.Equal(t, 1, e.standing.asked)
}

func TestThePacerLimitsAttemptsPerSecondAcrossPasses(t *testing.T) {
	rate := int32(1)
	e := driveFixture(t, 3, FleetSafetyRequest{MaxPostsPerSecond: &rate})
	require.Equal(t, 1, e.drive(t).Posted)
	// Another pass within the same second is out of permits: it waits and says why.
	r := e.drive(t)
	require.Zero(t, r.Posted)
	require.Equal(t, FleetHoldRateLimit, r.Hold)
	hold, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Equal(t, FleetHoldRateLimit, hold.HoldCode)
	require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
	// A second later the window has rolled.
	e.now = e.now.Add(1100 * time.Millisecond)
	require.Equal(t, 1, e.drive(t).Posted)
	e.now = e.now.Add(1100 * time.Millisecond)
	r = e.drive(t)
	require.Equal(t, 1, r.Posted)
	require.True(t, r.Dispatched)
	hold, err = e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Empty(t, hold.HoldCode, "a pass that was not held clears the hold")
}

func TestPermitsNeverExceedWhatTheWindowAllows(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	total := 0
	for i := 0; i < 20; i++ {
		g, err := e.api.AcquireFleetPermits(e.ctx, e.op.ID, 3, 5, e.now)
		require.NoError(t, err)
		total += g
	}
	require.Equal(t, 5, total, "twenty callers in one window share five permits")
	e.now = e.now.Add(time.Second + time.Millisecond)
	g, err := e.api.AcquireFleetPermits(e.ctx, e.op.ID, 3, 5, e.now)
	require.NoError(t, err)
	require.Equal(t, 3, g, "a new window starts from zero")
	g, err = e.api.AcquireFleetPermits(e.ctx, e.op.ID, 10, 5, e.now)
	require.NoError(t, err)
	require.Equal(t, 2, g)
	g, err = e.api.AcquireFleetPermits(e.ctx, e.op.ID, 0, 5, e.now)
	require.NoError(t, err)
	require.Zero(t, g)
}

func TestTheCapsAreSharedAmongRunningOperations(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	got, err := e.api.fleetAllowed(e.ctx, e.current(t), FleetCaps{TenantPostsPerSecond: 50, InstancePostsPerSecond: 500})
	require.NoError(t, err)
	require.Equal(t, 10, got, "the operation's own rate is the smallest")
	got, err = e.api.fleetAllowed(e.ctx, e.current(t), FleetCaps{TenantPostsPerSecond: 4, InstancePostsPerSecond: 500})
	require.NoError(t, err)
	require.Equal(t, 4, got, "one running operation takes the tenant cap")
	// A second running operation in the tenant halves it.
	e.second(t)
	got, err = e.api.fleetAllowed(e.ctx, e.current(t), FleetCaps{TenantPostsPerSecond: 4, InstancePostsPerSecond: 500})
	require.NoError(t, err)
	require.Equal(t, 2, got)
	got, err = e.api.fleetAllowed(e.ctx, e.current(t), FleetCaps{TenantPostsPerSecond: 50, InstancePostsPerSecond: 2})
	require.NoError(t, err)
	require.Equal(t, 1, got, "the instance cap is shared too, and an operation always gets at least one")
}

// second starts a second operation in the tenant over the same device.
func (e *driveEnv) second(t *testing.T) {
	t.Helper()
	v := "v2"
	op, err := e.api.CreateFleetOperation(e.ctx, &FleetOperationCreateRequest{Token: "drv2", Name: "n", Kind: FleetKindConfigSet,
		DeviceTokens: []string{e.devs[0].Token}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v}}, "creator")
	require.NoError(t, err)
	_, err = e.api.StartFleetOperation(e.ctx, "drv2", op.ContractDigest, alice)
	require.NoError(t, err)
}

func TestARevokedGranteePausesTheOperationAndPostsNothing(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	e.standing.answer[alice.Name] = false
	r := e.drive(t)
	require.Equal(t, FleetPauseAuthorityRevoked, r.Paused)
	require.Zero(t, r.Posted)
	op := e.current(t)
	require.Equal(t, FleetDispatchPaused, op.Dispatch)
	require.Equal(t, FleetPauseAuthorityRevoked, op.PauseReason)
	require.Greater(t, op.ExecutionEpoch, e.op.ExecutionEpoch)
	require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
	// Anyone holding the authorities resumes it and becomes the grantee; the pause is not a failure.
	bob := FleetApprover{Name: "bob@example.com", Authorities: alice.Authorities}
	_, err := e.api.ResumeFleetOperation(e.ctx, "drv", e.op.ContractDigest, nil, bob)
	require.NoError(t, err)
	e.standing.answer = map[string]bool{bob.Name: true}
	require.Equal(t, 2, e.drive(t).Posted)
}

func TestAnUnavailableAnswerHoldsAndNeverPassesOrPauses(t *testing.T) {
	for name, setup := range map[string]func(e *driveEnv){
		"an error":        func(e *driveEnv) { e.standing.err = errors.New("user-management is down") },
		"no answer":       func(e *driveEnv) { e.standing.answer = nil },
		"another subject": func(e *driveEnv) { e.standing.answer = map[string]bool{"someone-else": true} },
		"no checker":      func(e *driveEnv) { e.api.PrincipalStanding = nil },
	} {
		t.Run(name, func(t *testing.T) {
			e := driveFixture(t, 2, FleetSafetyRequest{})
			setup(e)
			r := e.drive(t)
			require.Equal(t, FleetHoldAuthorityUnavailable, r.Hold)
			require.Empty(t, r.Paused, "an answer that is not an answer is not a revocation")
			require.Zero(t, r.Posted)
			require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch)
			require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
			rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
			require.NoError(t, err)
			require.Equal(t, FleetHoldAuthorityUnavailable, rt.HoldCode)
			require.True(t, rt.HoldSince.Valid)
		})
	}
}

func TestTheLeaseIsGoodForFiveSecondsFromTheMomentTheQuestionWasSent(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	before := time.Now()
	lease, hold, err := e.api.fleetLease(e.ctx, e.current(t))
	require.NoError(t, err)
	require.Empty(t, hold)
	require.NotNil(t, lease)
	require.False(t, lease.Deadline.Before(before.Add(FleetLeaseTTL)))
	require.True(t, lease.Deadline.Before(time.Now().Add(FleetLeaseTTL+time.Second)))
	require.Equal(t, e.current(t).ExecutionEpoch, lease.Epoch)
	require.Equal(t, alice.Name, lease.Grantee)
}

func TestAFailureLimitAtItsCountPausesBeforeAnotherLineIsPosted(t *testing.T) {
	fail := int32(1)
	e := driveFixture(t, 3, FleetSafetyRequest{FailureThreshold: &fail})
	var first FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).Order("ordinal").First(&first).Error)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("id = ?", first.ID).
		Updates(map[string]any{"posting": FleetPostingPosted, "settlement": string(ledger.SettlementFailed)}).Error)
	r := e.drive(t)
	require.Equal(t, FleetPauseFailureThreshold, r.Paused)
	require.Zero(t, r.Posted)
	require.Equal(t, FleetPauseFailureThreshold, e.current(t).PauseReason)
	require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
	// Negative control: below the limit the pass posts.
	e2 := driveFixture(t, 2, FleetSafetyRequest{FailureThreshold: &fail})
	require.Equal(t, 2, e2.drive(t).Posted)
}

func TestAnAmbiguityLimitPausesToo(t *testing.T) {
	amb := int32(1)
	e := driveFixture(t, 2, FleetSafetyRequest{AmbiguityThreshold: &amb})
	var first FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).Order("ordinal").First(&first).Error)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("id = ?", first.ID).
		Updates(map[string]any{"posting": FleetPostingPosted, "settlement": string(ledger.SettlementAmbiguous)}).Error)
	require.Equal(t, FleetPauseAmbiguity, e.drive(t).Paused)
}

func TestALineBackingOffIsNotHintedUntilItIsDue(t *testing.T) {
	e := driveFixture(t, 3, FleetSafetyRequest{})
	var first FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).Order("ordinal").First(&first).Error)
	require.NoError(t, e.api.RecordFleetLineRetry(e.ctx, first.ID, "DEADLOCK", e.now))
	r := e.drive(t)
	require.Equal(t, 2, r.Posted, "the other two post; the one backing off waits")
	require.False(t, r.Dispatched, "a line is still PLANNED")
	require.Equal(t, FleetPostingPlanned, e.reloadLine(t, first.ID).Posting)
	e.now = e.now.Add(3 * time.Second)
	r = e.drive(t)
	require.Equal(t, 1, r.Posted)
	require.True(t, r.Dispatched)
}

func TestABusyHeadBacksOffAndDueLinesKeepTheirOrder(t *testing.T) {
	for _, busy := range []string{"DEVICE_BUSY", "ATTRIBUTE_ROW_BUSY"} {
		t.Run(busy, func(t *testing.T) {
			rate := int32(1)
			e := driveFixture(t, 3, FleetSafetyRequest{MaxPostsPerSecond: &rate})
			var lines []FleetOperationLine
			require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).Order("ordinal").Find(&lines).Error)
			// A repeatedly busy head gets at least four seconds of backoff, enough to
			// inspect several fresh permit windows without depending on random jitter.
			require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("id = ?", lines[0].ID).
				Update("attempts", 2).Error)
			head := uint(lines[0].TargetId.Int64)
			original := fleetTryDeviceLock
			fleetTryDeviceLock = func(ctx context.Context, tx *gorm.DB, id uint) (bool, error) {
				if id == head {
					if busy == "DEVICE_BUSY" {
						return false, nil
					}
					return false, &AttributeWriteBusyError{}
				}
				return original(ctx, tx, id)
			}
			t.Cleanup(func() { fleetTryDeviceLock = original })

			r := e.drive(t)
			require.Equal(t, 1, r.Busy+r.Retried)
			require.Zero(t, r.Posted)
			line := e.reloadLine(t, lines[0].ID)
			require.Equal(t, FleetPostingPlanned, line.Posting)
			require.Equal(t, int32(3), line.Attempts)
			require.Equal(t, busy, line.LastErrorCode)
			require.True(t, line.NextAttemptAt.Valid)
			require.True(t, line.NextAttemptAt.Time.After(e.now))
			// Later due lines must advance in the original order during that backoff,
			// rather than the head spending each window's only permit.
			for i := 1; i < len(lines); i++ {
				e.now = e.now.Add(time.Second)
				require.Equal(t, 1, e.drive(t).Posted)
				require.Equal(t, FleetPostingPosted, e.reloadLine(t, lines[i].ID).Posting)
				if i+1 < len(lines) {
					require.Equal(t, FleetPostingPlanned, e.reloadLine(t, lines[i+1].ID).Posting)
				}
			}
			// A pass with only future work must leave the permit count/window untouched.
			before, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
			require.NoError(t, err)
			e.now = e.now.Add(time.Second)
			r = e.drive(t)
			require.Zero(t, r.Posted+r.Busy+r.Retried)
			after, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
			require.NoError(t, err)
			require.Equal(t, before.PaceIssued, after.PaceIssued)
			require.Equal(t, before.PaceWindowStart, after.PaceWindowStart)
			require.Equal(t, int32(3), e.reloadLine(t, line.ID).Attempts)
			fleetTryDeviceLock = original
			e.now = e.reloadLine(t, line.ID).NextAttemptAt.Time
			r = e.drive(t)
			require.Equal(t, 1, r.Posted)
			require.True(t, r.Dispatched)
		})
	}
}

func (e *driveEnv) reloadLine(t *testing.T, id uint) FleetOperationLine {
	t.Helper()
	var l FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).First(&l, id).Error)
	return l
}

func TestADecisionTakenUnderAnOldEpochIsNeverAppliedToANewerState(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	op := e.current(t)
	// A person paused and resumed meanwhile: the epoch moved on.
	_, err := e.api.PauseFleetOperation(e.ctx, "drv", alice.Name)
	require.NoError(t, err)
	_, err = e.api.ResumeFleetOperation(e.ctx, "drv", op.ContractDigest, nil, alice)
	require.NoError(t, err)
	_, err = e.api.fleetAutoPause(e.ctx, "drv", op.ExecutionEpoch, FleetPauseFailureThreshold)
	requireFleetCode(t, err, FleetCodeIllegalTransition)
	require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch)
}

func TestAnOperationWithNothingToPostFinishesAtOnce(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).
		Update("posting", FleetPostingRefused).Error)
	r := e.drive(t)
	require.True(t, r.Dispatched)
	op := e.current(t)
	require.Equal(t, FleetDispatchDispatched, op.Dispatch)
	require.Equal(t, "CLOSED", op.Evidence, "nothing was applied, so there is no evidence to wait for")
}

func TestAPassPostsNoMoreLinesThanItWasGrantedPermitsFor(t *testing.T) {
	rate := int32(3)
	e := driveFixture(t, 5, FleetSafetyRequest{MaxPostsPerSecond: &rate})
	// Another replica already used two of this second's three permits.
	g, err := e.api.AcquireFleetPermits(e.ctx, e.op.ID, 2, 3, e.now)
	require.NoError(t, err)
	require.Equal(t, 2, g)
	r := e.drive(t)
	require.Equal(t, 1, r.Posted, "one permit was left, so one attempt was made")
	require.Equal(t, int64(4), e.count(t, FleetPostingPlanned))
}

func TestTheOperationRestsInDispatchedOnlyWhenNoLineIsPlannedUnderTheFence(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	// A pass that counted nothing left, but a line is PLANNED by the time it holds the fence.
	res, err := e.api.finishDriven(e.ctx, &FleetDriveResult{}, e.current(t))
	require.NoError(t, err)
	require.False(t, res.Dispatched)
	require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch)
}

type slowStanding struct {
	delay time.Duration
}

func (s slowStanding) PrincipalStanding(_ context.Context, principals, _ []string) (map[string]bool, error) {
	time.Sleep(s.delay)
	return map[string]bool{principals[0]: true}, nil
}

func TestASlowAnswerDoesNotStretchTheLease(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	e.api.PrincipalStanding = slowStanding{delay: 600 * time.Millisecond}
	lease, hold, err := e.api.fleetLease(e.ctx, e.current(t))
	require.NoError(t, err)
	require.Empty(t, hold)
	// The question went out 600ms ago: what is left of the five seconds is at most 4.4.
	require.Less(t, time.Until(lease.Deadline), FleetLeaseTTL-500*time.Millisecond)
}

func TestAHoldKeepsItsStartAcrossPassesAndIsClearedOnlyByAPassThatIsNotHeld(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	e.standing.err = errors.New("user-management is down")
	require.Equal(t, FleetHoldAuthorityUnavailable, e.drive(t).Hold)
	first, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.True(t, first.HoldSince.Valid)
	e.now = e.now.Add(5 * time.Second)
	require.Equal(t, FleetHoldAuthorityUnavailable, e.drive(t).Hold)
	again, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.True(t, first.HoldSince.Time.Equal(again.HoldSince.Time), "how long it has been held is not reset by looking again")
	require.True(t, again.DriverSeenAt.Time.After(first.DriverSeenAt.Time), "but the heartbeat moves")
	// The answer returns: the pass is not held, and the hold is cleared.
	e.standing.err = nil
	require.Equal(t, 2, e.drive(t).Posted)
	cleared, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Empty(t, cleared.HoldCode)
	require.False(t, cleared.HoldSince.Valid)
}

func TestAnUndecidedPostErrorBacksOffWithoutAbortingThePass(t *testing.T) {
	e := driveFixture(t, 4, FleetSafetyRequest{})
	var first FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).Order("ordinal").First(&first).Error)
	undecided := errors.New("undecided posting failure")
	attempts := 0
	attributeBeforeCommitHook = func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&FleetOperationLine{}).Where("id = ? AND posting = ?", first.ID, FleetPostingPosted).Count(&n).Error; err != nil {
			return err
		}
		if n == 1 {
			attempts++
			return undecided
		}
		return nil
	}
	t.Cleanup(func() { attributeBeforeCommitHook = nil })

	r, err := e.api.DriveFleetOperation(e.ctx, "drv", DefaultFleetCaps)
	require.ErrorIs(t, err, undecided, "the original failure remains observable")
	require.Equal(t, 3, r.Posted, "all later lines still advance")
	require.Equal(t, "POST_ERROR", r.Hold)
	require.False(t, r.Dispatched)
	require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch)
	l := e.reloadLine(t, first.ID)
	require.Equal(t, FleetPostingPlanned, l.Posting)
	require.Equal(t, "POST_ERROR", l.LastErrorCode, "an undecided failure has its own code, distinct from retryable errors and refusals")
	require.Equal(t, int32(1), l.Attempts)
	require.True(t, l.NextAttemptAt.Valid)
	require.GreaterOrEqual(t, l.NextAttemptAt.Time.Sub(e.now), time.Second)
	require.Less(t, l.NextAttemptAt.Time.Sub(e.now), 2*time.Second)
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.True(t, rt.DriverSeenAt.Valid)
	require.True(t, rt.DriverSeenAt.Time.Equal(e.now))
	require.True(t, rt.LastProgressAt.Valid)
	require.True(t, rt.LastProgressAt.Time.Equal(e.now))
	require.Equal(t, "POST_ERROR", rt.HoldCode)
	require.True(t, rt.HoldSince.Valid)
	require.True(t, rt.HoldSeenAt.Valid)

	// Just before the recorded deadline, another pass must not try the broken line, and the hold stands
	// with its start unmoved: waiting out the backoff is still waiting on the undecided line.
	heldSince := rt.HoldSince.Time
	e.now = l.NextAttemptAt.Time.Add(-time.Nanosecond)
	waiting := e.drive(t)
	require.Zero(t, waiting.Posted)
	require.Equal(t, FleetHoldPostError, waiting.Hold)
	require.Equal(t, 1, attempts)
	require.Equal(t, int32(1), e.reloadLine(t, first.ID).Attempts)
	rt, err = e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Equal(t, FleetHoldPostError, rt.HoldCode)
	require.True(t, rt.HoldSince.Time.Equal(heldSince), "a backoff pass does not restart the hold")
	// Once due, a repaired dependency can make progress and clear the hold.
	attributeBeforeCommitHook = nil
	e.now = l.NextAttemptAt.Time.Add(time.Second)
	r = e.drive(t)
	require.Equal(t, 1, r.Posted)
	require.True(t, r.Dispatched)
	rt, err = e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Empty(t, rt.HoldCode)
}

func TestEarlyPassReturnsClearStaleHolds(t *testing.T) {
	for _, path := range []string{"backoff", "finished", "revoked", "threshold"} {
		t.Run(path, func(t *testing.T) {
			e := driveFixture(t, 1, FleetSafetyRequest{})
			e.standing.err = errors.New("standing unavailable")
			require.Equal(t, FleetHoldAuthorityUnavailable, e.drive(t).Hold)
			e.standing.err = nil
			e.now = e.now.Add(time.Second)
			switch path {
			case "backoff":
				var line FleetOperationLine
				require.NoError(t, e.api.RDB.DB(e.ctx).Where("operation_id = ?", e.op.ID).First(&line).Error)
				require.NoError(t, e.api.RecordFleetLineRetry(e.ctx, line.ID, "DEVICE_BUSY", e.now))
			case "finished":
				require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).Update("posting", FleetPostingRefused).Error)
			case "revoked":
				e.standing.answer[alice.Name] = false
			case "threshold":
				require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{}).Where("id = ?", e.op.ID).Update("eff_failure_threshold", 1).Error)
				require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).Update("posting", FleetPostingRefused).Error)
			}
			r := e.drive(t)
			require.Empty(t, r.Hold)
			rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
			require.NoError(t, err)
			require.Empty(t, rt.HoldCode)
			require.False(t, rt.HoldSince.Valid)
			require.False(t, rt.HoldSeenAt.Valid)
			require.True(t, rt.DriverSeenAt.Time.Equal(e.now))
		})
	}
}

func TestAPassThatFailsBeforeDecidingLeavesTheHoldAlone(t *testing.T) {
	rate := int32(1)
	e := driveFixture(t, 3, FleetSafetyRequest{MaxPostsPerSecond: &rate})
	require.Equal(t, 1, e.drive(t).Posted)
	e.now = e.now.Add(100 * time.Millisecond)
	require.Equal(t, FleetHoldRateLimit, e.drive(t).Hold)
	held, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Equal(t, FleetHoldRateLimit, held.HoldCode)

	// The next pass gets its lease and then fails reading the lines: it decided nothing about the hold.
	broken := errors.New("lines unreadable")
	failing := true
	require.NoError(t, e.api.RDB.DB(e.ctx).Callback().Query().After("gorm:query").Register("test:fail_lines", func(d *gorm.DB) {
		if failing && d.Statement.Table == "fleet_operation_lines" {
			_ = d.AddError(broken)
		}
	}))
	t.Cleanup(func() { _ = e.api.RDB.DB(e.ctx).Callback().Query().Remove("test:fail_lines") })
	e.now = e.now.Add(100 * time.Millisecond)
	_, err = e.api.DriveFleetOperation(e.ctx, "drv", DefaultFleetCaps)
	require.ErrorIs(t, err, broken)
	failing = false
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Equal(t, FleetHoldRateLimit, rt.HoldCode, "a failed pass does not clear a hold it never re-examined")
	require.True(t, rt.HoldSince.Time.Equal(held.HoldSince.Time))
	require.True(t, rt.DriverSeenAt.Time.Equal(e.now), "the heartbeat still moves")

	// Held again on the next pass, the hold keeps its original start.
	e.now = e.now.Add(100 * time.Millisecond)
	require.Equal(t, FleetHoldRateLimit, e.drive(t).Hold)
	rt, err = e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.True(t, rt.HoldSince.Time.Equal(held.HoldSince.Time), "how long it has been held survives a failed pass")
}
