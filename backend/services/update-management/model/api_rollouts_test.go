// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// fakeGroups is device-management's group resolver: tenant/group to its members, paged.
type fakeGroups struct {
	mu       sync.Mutex
	members  map[string][]string
	rejected map[string]string
	version  *int32
	err      error
	pageSize int
}

func (f *fakeGroups) set(tenant, group string, devices ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.members == nil {
		f.members = map[string][]string{}
	}
	f.members[tenant+"/"+group] = devices
}

func (f *fakeGroups) ResolveGroupTargets(ctx context.Context, group string, _ *int32, after string, limit int) (*GroupTargetPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	tenant, _ := core.TenantFromContext(ctx)
	if code, ok := f.rejected[tenant+"/"+group]; ok {
		return &GroupTargetPage{Rejected: true, Code: code, Reason: "rejected by the fake"}, nil
	}
	all, ok := f.members[tenant+"/"+group]
	if !ok {
		return &GroupTargetPage{Rejected: true, Code: "GROUP_NOT_FOUND", Reason: "no such group"}, nil
	}
	if f.pageSize > 0 && f.pageSize < limit {
		limit = f.pageSize
	}
	start := 0
	if after != "" {
		fmt.Sscanf(after, "%d", &start)
	}
	end := min(start+limit, len(all))
	page := &GroupTargetPage{DeviceTokens: append([]string(nil), all[start:end]...), ResolvedVersion: f.version}
	if end < len(all) {
		page.NextCursor = fmt.Sprint(end)
	}
	return page, nil
}

// syncDevices is a concurrency-safe device lookup: every device it knows is a gateway.
type syncDevices struct {
	mu       sync.Mutex
	profiles map[string]string
	disabled map[string]bool
	err      error
}

func (s *syncDevices) DeviceOf(_ context.Context, tenant, device string) (DeviceFacts, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return DeviceFacts{}, false, s.err
	}
	p, ok := s.profiles[tenant+"/"+device]
	return DeviceFacts{Profile: p, Disabled: s.disabled[tenant+"/"+device], Anchors: []string{}}, ok, nil
}

type rolloutEnv struct {
	*updateEnv
	groups  *fakeGroups
	devices *syncDevices
}

// newRolloutEnv is the update env with a group resolver and n gateway devices dev-0..dev-(n-1) in
// acme's group "fleet", and one AVAILABLE artifact fw-2.
func newRolloutEnv(t *testing.T, n int) *rolloutEnv {
	t.Helper()
	e := &rolloutEnv{updateEnv: newUpdateEnv(t), groups: &fakeGroups{}, devices: &syncDevices{
		profiles: map[string]string{}, disabled: map[string]bool{}}}
	var fleet []string
	for i := range n {
		d := fmt.Sprintf("dev-%d", i)
		fleet = append(fleet, d)
		e.devices.profiles["acme/"+d] = "gateway"
	}
	e.devices.profiles["globex/dev-0"] = "gateway"
	e.groups.set("acme", "fleet", fleet...)
	e.groups.set("globex", "fleet", "dev-0")
	e.api.Devices = e.devices
	e.api.Groups = e.groups
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	return e
}

func (e *rolloutEnv) create(t *testing.T, token string, maxInFlight int, threshold *int) {
	t.Helper()
	_, err := e.api.CreateRollout(tenantCtx("acme"), RolloutCreateRequest{Token: token, ArtifactToken: "fw-2",
		GroupToken: "fleet", MaxInFlight: maxInFlight, FailureThreshold: threshold})
	require.NoError(t, err)
}

func (e *rolloutEnv) rollout(t *testing.T, token string) *UpdateRolloutView {
	t.Helper()
	v, err := e.api.RolloutByToken(tenantCtx("acme"), token)
	require.NoError(t, err)
	require.NotNil(t, v)
	return v
}

func (e *rolloutEnv) step(t *testing.T, token string) RolloutStepResult {
	t.Helper()
	res, err := e.api.StepRollout(tenantCtx("acme"), e.rollout(t, token).Rollout.ID)
	require.NoError(t, err)
	return res
}

// finishCurrent moves the current attempt of each device to state, through the attempt row the
// reducer owns (the shortcut is the write; what the rollout reads is the same column).
func (e *rolloutEnv) finishCurrent(t *testing.T, state ota.State, devices ...string) {
	t.Helper()
	ctx := tenantCtx("acme")
	for _, d := range devices {
		var asg []UpdateAssignment
		require.NoError(t, e.api.RDB.DB(ctx).Where("device_token = ? AND superseded_at IS NULL", d).Find(&asg).Error)
		require.Len(t, asg, 1, "device %s has one current assignment", d)
		require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("assignment_id = ?", asg[0].ID).
			Update("state", string(state)).Error)
	}
}

func (e *rolloutEnv) targets(t *testing.T, token string, outcome *string) []RolloutTargetView {
	t.Helper()
	res, err := e.api.RolloutTargets(tenantCtx("acme"), token, RolloutTargetCriteria{
		Pagination: paginationAll(), Outcome: outcome})
	require.NoError(t, err)
	require.NotNil(t, res)
	return res.Results
}

func (e *rolloutEnv) assignmentCount(t *testing.T, device string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Model(&UpdateAssignment{}).Where("device_token = ?", device).Count(&n).Error)
	return n
}

func TestAStartedRolloutDispatchesExactlyTheCapAndPublishesEachAssignment(t *testing.T) {
	e := newRolloutEnv(t, 5)
	e.create(t, "r1", 2, nil)
	require.Equal(t, RolloutDraft, e.rollout(t, "r1").Rollout.State)
	require.Zero(t, e.assignmentCount(t, "dev-0"), "a DRAFT rollout tells no device anything")

	v, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Equal(t, RolloutRunning, v.Rollout.State)
	require.Equal(t, 5, v.Rollout.TargetCount)
	require.Equal(t, RolloutProgress{Total: 5, Pending: 3, InFlight: 2}, v.Progress, "the start's inline step fills the cap")
	require.Equal(t, []string{ota.KindUpdateAssignment}, e.desired.kinds(t, "dev-0"))
	require.Equal(t, []string{ota.KindUpdateAssignment}, e.desired.kinds(t, "dev-1"))
	require.Empty(t, e.desired.kinds(t, "dev-2"), "the third device waits for a slot")

	// A step at the cap dispatches nothing.
	require.Equal(t, RolloutStepResult{}, e.step(t, "r1"))
	require.Equal(t, 2, e.rollout(t, "r1").Progress.Occupied())
}

func TestFinishedAttemptsFreeSlotsAndTheLastOneCompletesTheRollout(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 2, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)

	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	require.Equal(t, 1, e.step(t, "r1").Dispatched, "one freed slot, one dispatch")
	require.Equal(t, RolloutProgress{Total: 3, InFlight: 2, Succeeded: 1}, e.rollout(t, "r1").Progress)

	e.finishCurrent(t, ota.StateUpdated, "dev-1")
	e.finishCurrent(t, ota.StateFailed, "dev-2")
	res := e.step(t, "r1")
	require.Equal(t, RolloutCompleted, res.Transition)
	v := e.rollout(t, "r1")
	require.Equal(t, RolloutCompleted, v.Rollout.State)
	require.True(t, v.Rollout.FinishedAt.Valid)
	require.Equal(t, RolloutProgress{Total: 3, Succeeded: 2, Failed: 1}, v.Progress)
	require.Equal(t, RolloutStepResult{}, e.step(t, "r1"), "a completed rollout is not stepped again")

	// The outcome filter reads the attempts.
	failed := e.targets(t, "r1", ptr(OutcomeFailed))
	require.Len(t, failed, 1)
	require.Equal(t, "dev-2", failed[0].Target.DeviceToken)
	require.Equal(t, string(ota.StateFailed), failed[0].AttemptState)
	require.NotEmpty(t, failed[0].AssignmentToken)
}

// UNKNOWN holds its slot: a device that may have installed and not come back must not free
// capacity for the next one.
func TestAnUnknownOutcomeHoldsItsSlot(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUnknown, "dev-0")
	require.Zero(t, e.step(t, "r1").Dispatched)
	// The control: the same device ending FAILED frees the slot.
	e.finishCurrent(t, ota.StateFailed, "dev-0")
	require.Equal(t, 1, e.step(t, "r1").Dispatched)
}

func TestPauseStopsNewDispatchAndResumeRestartsIt(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.create(t, "r1", 2, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)

	v, err := e.api.PauseRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Equal(t, RolloutPaused, v.Rollout.State)
	require.Equal(t, PauseOperator, v.Rollout.PauseReason)
	require.Equal(t, "alice", v.Rollout.StateChangedBy)
	_, err = e.api.PauseRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err, "pausing a paused rollout is a no-op")

	e.finishCurrent(t, ota.StateUpdated, "dev-0", "dev-1")
	require.Equal(t, RolloutStepResult{}, e.step(t, "r1"), "a paused rollout dispatches nothing, however many slots are free")
	require.Zero(t, e.assignmentCount(t, "dev-2"))
	// The in-flight devices were not recalled.
	require.NotContains(t, e.desired.kinds(t, "dev-0"), ota.KindUpdateCancel)

	v, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", nil)
	require.NoError(t, err)
	require.Equal(t, RolloutRunning, v.Rollout.State)
	require.Empty(t, v.Rollout.PauseReason)
	require.Equal(t, RolloutProgress{Total: 4, InFlight: 2, Succeeded: 2}, v.Progress, "the resume steps at once")
}

func TestAbortCancelsThePendingAndLeavesTheInFlightToFinish(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.create(t, "r1", 2, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)

	v, err := e.api.AbortRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Equal(t, RolloutAborted, v.Rollout.State)
	require.Equal(t, RolloutProgress{Total: 4, InFlight: 2, Cancelled: 2}, v.Progress,
		"the in-flight devices are reported in flight, not claimed stopped")
	for _, d := range []string{"dev-0", "dev-1"} {
		require.NotContains(t, e.desired.kinds(t, d), ota.KindUpdateCancel, "abort sends %s no cancel", d)
		views, err := e.api.CurrentUpdates(tenantCtx("acme"), d)
		require.NoError(t, err)
		require.Len(t, views, 1, "%s keeps its live assignment", d)
		require.False(t, views[0].Attempt.CancelRequested)
	}
	require.Equal(t, RolloutStepResult{}, e.step(t, "r1"))
	require.Zero(t, e.assignmentCount(t, "dev-2"))

	_, err = e.api.AbortRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err, "aborting an aborted rollout is a no-op")
	_, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", nil)
	requireRefusal(t, err, CodeRolloutStateConflict)

	// The in-flight devices finish after the abort, and the progress keeps telling the truth.
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	require.Equal(t, 1, e.rollout(t, "r1").Progress.Succeeded)
}

// A rollout pauses itself when its failed and unknown devices reach the threshold, through the
// REAL failure path (the deadline sweep times the quiet devices out). Resuming does not reset the
// count: it is refused until the threshold is raised.
func TestTheFailureThresholdPausesAndResumeDoesNotResetTheCount(t *testing.T) {
	e := newRolloutEnv(t, 5)
	e.create(t, "r1", 2, ptr(2))
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)

	e.advance(testPolicy.Acknowledge + time.Minute)
	swept, err := e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 2, swept.TimedOut)

	res := e.step(t, "r1")
	require.Equal(t, RolloutPaused, res.Transition)
	require.Equal(t, PauseFailureThreshold, res.Reason)
	v := e.rollout(t, "r1")
	require.Equal(t, PauseFailureThreshold, v.Rollout.PauseReason)
	require.Empty(t, v.Rollout.StateChangedBy, "the rollout paused itself")
	require.Zero(t, e.assignmentCount(t, "dev-2"), "nothing is dispatched past the threshold")

	_, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", nil)
	requireRefusal(t, err, CodeRolloutThresholdReached)
	_, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", ptr(2))
	requireRefusal(t, err, CodeRolloutThresholdReached)

	v, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", ptr(3))
	require.NoError(t, err)
	require.Equal(t, RolloutRunning, v.Rollout.State)
	require.Equal(t, int32(3), v.Rollout.FailureThreshold.Int32)
	require.Equal(t, 2, v.Progress.InFlight, "the raised threshold lets two more go")
}

// Without a threshold, failures never pause: the control for the test above.
func TestWithoutAThresholdFailuresDoNotPause(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateFailed, "dev-0")
	res := e.step(t, "r1")
	require.Empty(t, res.Transition)
	require.Equal(t, 1, res.Dispatched)
}

// The rollout lives in rows: an Api built afresh over the same database (a restarted replica)
// carries on from where the last step left off and dispatches no device twice.
func TestARestartedServiceResumesTheRolloutWithoutRedispatching(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.create(t, "r1", 2, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")

	fresh := NewApi(e.api.RDB, e.store, e.api.Limits)
	fresh.Profiles, fresh.Devices, fresh.Groups, fresh.Desired = e.api.Profiles, e.devices, e.groups, e.desired
	fresh.Policy, fresh.AllowUnverified, fresh.Clock = e.api.Policy, true, e.api.Clock
	adv, err := fresh.AdvanceRollouts(context.Background())
	require.NoError(t, err)
	require.Equal(t, AdvanceResult{Stepped: 1, Dispatched: 1}, adv)
	for _, d := range []string{"dev-0", "dev-1", "dev-2"} {
		require.Equal(t, int64(1), e.assignmentCount(t, d), "%s was assigned exactly once", d)
	}
	require.Zero(t, e.assignmentCount(t, "dev-3"))
}

// The target set is the group as of the START: a device that joins later is not part of the
// rollout, and one that leaves stays a target.
func TestGroupChangesAfterTheStartDoNotChangeTheTargetSet(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 1, nil)
	e.groups.set("acme", "fleet", "dev-0", "dev-1") // dev-2 left before the start
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Equal(t, 2, e.rollout(t, "r1").Rollout.TargetCount)

	e.devices.profiles["acme/dev-9"] = "gateway"
	e.groups.set("acme", "fleet", "dev-0", "dev-9") // dev-1 leaves, dev-9 joins
	for _, d := range []string{"dev-0", "dev-1"} {
		e.finishCurrent(t, ota.StateUpdated, d)
		e.step(t, "r1")
	}
	require.Equal(t, RolloutCompleted, e.rollout(t, "r1").Rollout.State)
	require.Equal(t, int64(1), e.assignmentCount(t, "dev-1"), "the leaver stayed a target")
	require.Zero(t, e.assignmentCount(t, "dev-9"), "the joiner is not part of this rollout")
	require.Zero(t, e.assignmentCount(t, "dev-2"))
}

func TestRefusedDevicesAreSkippedWithTheAssignmentPlanesCodeAndHoldNoSlot(t *testing.T) {
	e := newRolloutEnv(t, 6)
	ctx := tenantCtx("acme")
	delete(e.devices.profiles, "acme/dev-0")    // gone
	e.devices.disabled["acme/dev-1"] = true     // quarantined
	e.devices.profiles["acme/dev-2"] = "sensor" // incompatible
	e.availableArtifact(t, "acme", "fw-other", "9.0.0", []byte("another image"))
	_, err := e.api.AssignArtifact(ctx, "dev-3", "fw-other") // busy with another artifact
	require.NoError(t, err)
	pre, err := e.api.AssignArtifact(ctx, "dev-4", "fw-2") // already on its way to THIS artifact
	require.NoError(t, err)

	e.create(t, "r1", 2, nil)
	_, err = e.api.StartRollout(ctx, "r1")
	require.NoError(t, err)

	skipped := map[string]string{}
	for _, tv := range e.targets(t, "r1", ptr(OutcomeSkipped)) {
		skipped[tv.Target.DeviceToken] = tv.Target.SkipCode
	}
	require.Equal(t, map[string]string{"dev-0": CodeDeviceNotFound, "dev-1": CodeDeviceDisabled,
		"dev-2": CodeIncompatibleProfile, "dev-3": CodeUpdateInProgress}, skipped)
	inFlight := e.targets(t, "r1", ptr(OutcomeInFlight))
	require.Len(t, inFlight, 2, "skips do not consume slots: both slots went to devices that can take it")
	require.Equal(t, "dev-4", inFlight[0].Target.DeviceToken)
	require.Equal(t, pre.Assignment.Token, inFlight[0].AssignmentToken, "the existing assignment is adopted, not repeated")
	require.Equal(t, int64(1), e.assignmentCount(t, "dev-4"))
	require.Equal(t, "dev-5", inFlight[1].Target.DeviceToken)
	require.Equal(t, int64(1), e.assignmentCount(t, "dev-3"), "the busy device keeps its own update")
}

func TestARetiredArtifactPausesTheRollout(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	_, err = e.api.RetireArtifact(tenantCtx("acme"), "fw-2")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")

	res := e.step(t, "r1")
	require.Equal(t, PauseArtifactNotAssignable, res.Reason)
	require.Zero(t, e.assignmentCount(t, "dev-1"))
	_, err = e.api.ResumeRollout(tenantCtx("acme"), "r1", nil)
	requireRefusal(t, err, CodeRolloutArtifactUnassignable)
}

func TestRolloutRefusals(t *testing.T) {
	e := newRolloutEnv(t, 2)
	ctx := tenantCtx("acme")
	req := func(mut func(*RolloutCreateRequest)) error {
		r := RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 1}
		mut(&r)
		_, err := e.api.CreateRollout(ctx, r)
		return err
	}
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.MaxInFlight = 0 }), CodeInvalidRollout)
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.MaxInFlight = MaxRolloutInFlight + 1 }), CodeInvalidRollout)
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.FailureThreshold = ptr(0) }), CodeInvalidRollout)
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.Token = "" }), CodeInvalidRollout)
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.ArtifactToken = "ghost" }), CodeArtifactNotFound)
	e.availableArtifact(t, "acme", "fw-old", "0.1.0", []byte("old image"))
	_, err := e.api.RetireArtifact(ctx, "fw-old")
	require.NoError(t, err)
	requireRefusal(t, req(func(r *RolloutCreateRequest) { r.ArtifactToken = "fw-old" }), CodeArtifactNotAvailable)
	e.api.AllowUnverified = false
	requireRefusal(t, req(func(*RolloutCreateRequest) {}), CodeUnverifiedRefused)
	e.api.AllowUnverified = true

	require.NoError(t, req(func(*RolloutCreateRequest) {}))
	requireRefusal(t, req(func(*RolloutCreateRequest) {}), CodeRolloutExists)

	_, err = e.api.PauseRollout(ctx, "r1")
	requireRefusal(t, err, CodeRolloutStateConflict)
	_, err = e.api.StartRollout(ctx, "ghost")
	requireRefusal(t, err, CodeRolloutNotFound)

	// The group's owner decides it cannot be a target; that is relayed, not an outage.
	e.groups.rejected = map[string]string{"acme/fleet": "NOT_A_DEVICE_GROUP"}
	_, err = e.api.StartRollout(ctx, "r1")
	requireRefusal(t, err, CodeRolloutGroupUnusable)
	e.groups.rejected = nil
	e.groups.err = errors.New("connection refused")
	_, err = e.api.StartRollout(ctx, "r1")
	requireRefusal(t, err, CodeRolloutGroupUnavailable)
	e.groups.err = nil
	e.groups.set("acme", "fleet")
	_, err = e.api.StartRollout(ctx, "r1")
	requireRefusal(t, err, CodeInvalidRollout)
	saved := e.api.Groups
	e.api.Groups = nil
	_, err = e.api.StartRollout(ctx, "r1")
	requireRefusal(t, err, CodeRolloutGroupUnavailable)
	e.api.Groups = saved
	require.Equal(t, RolloutDraft, e.rollout(t, "r1").Rollout.State, "no refused start moved it")

	e.groups.set("acme", "fleet", "dev-0")
	_, err = e.api.StartRollout(ctx, "r1")
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, "r1")
	requireRefusal(t, err, CodeRolloutStateConflict)
}

// A group larger than the ceiling is refused, never truncated; the walk follows the cursor across
// pages to see it.
func TestAGroupOverTheCeilingIsRefusedNotTruncated(t *testing.T) {
	e := newRolloutEnv(t, 1)
	big := make([]string, MaxRolloutTargets+1)
	for i := range big {
		big[i] = fmt.Sprintf("d-%d", i)
	}
	e.groups.set("acme", "fleet", big...)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	requireRefusal(t, err, CodeRolloutTooLarge)

	// The control: exactly the ceiling, over several short pages, is frozen whole.
	e.groups.pageSize = 700
	e.groups.set("acme", "fleet", big[:MaxRolloutTargets]...)
	_, err = e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Equal(t, MaxRolloutTargets, e.rollout(t, "r1").Rollout.TargetCount)
}

func TestRolloutsAreTenantIsolated(t *testing.T) {
	e := newRolloutEnv(t, 2)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)

	other := tenantCtx("globex")
	v, err := e.api.RolloutByToken(other, "r1")
	require.NoError(t, err)
	require.Nil(t, v)
	page, err := e.api.Rollouts(other, RolloutSearchCriteria{Pagination: paginationAll()})
	require.NoError(t, err)
	require.Empty(t, page.Results)
	targets, err := e.api.RolloutTargets(other, "r1", RolloutTargetCriteria{Pagination: paginationAll()})
	require.NoError(t, err)
	require.Nil(t, targets)
	for _, op := range []func() error{
		func() error { _, err := e.api.PauseRollout(other, "r1"); return err },
		func() error { _, err := e.api.ResumeRollout(other, "r1", nil); return err },
		func() error { _, err := e.api.AbortRollout(other, "r1"); return err },
		func() error { _, err := e.api.StartRollout(other, "r1"); return err },
	} {
		requireRefusal(t, op(), CodeRolloutNotFound)
	}
	require.Equal(t, RolloutRunning, e.rollout(t, "r1").Rollout.State)

	// The same token in another tenant is another rollout.
	e.availableArtifact(t, "globex", "fw-2", "2.0.0", []byte("firmware two"))
	_, err = e.api.CreateRollout(other, RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 1})
	require.NoError(t, err)
	_, err = e.api.StartRollout(other, "r1")
	require.NoError(t, err)
	gv, err := e.api.RolloutByToken(other, "r1")
	require.NoError(t, err)
	require.Equal(t, 1, gv.Rollout.TargetCount)
	require.Equal(t, 2, e.rollout(t, "r1").Rollout.TargetCount)
}

// A rollout of a tenant that has been deleted is neither stepped nor controlled.
func TestADeletedTenantsRolloutIsNotStepped(t *testing.T) {
	e := newRolloutEnv(t, 2)
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.api.TenantDeleted = func(tenant string) bool { return tenant == "acme" }
	adv, err := e.api.AdvanceRollouts(context.Background())
	require.NoError(t, err)
	require.Zero(t, adv.Stepped)
	_, err = e.api.PauseRollout(tenantCtx("acme"), "r1")
	requireRefusal(t, err, CodeTenantDeleted)
}

func paginationAll() rdb.Pagination { return rdb.Pagination{PageNumber: 1, PageSize: 1000} }

// An UNKNOWN outcome counts toward the failure threshold: a device that may be bricked must not
// read as healthy.
func TestAnUnknownOutcomeCountsTowardTheThreshold(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 2, ptr(1))
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUnknown, "dev-0")
	res := e.step(t, "r1")
	require.Equal(t, RolloutPaused, res.Transition)
	require.Equal(t, PauseFailureThreshold, res.Reason)
}
