// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
)

// fakeDevices maps tenant/device to a profile token.
type fakeDevices struct {
	profiles map[string]string
	err      error
}

func (f *fakeDevices) ProfileOf(_ context.Context, tenant, device string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	p, ok := f.profiles[tenant+"/"+device]
	return p, ok, nil
}

type published struct {
	device string
	body   []byte
}

// recorder is the device-desired channel: it keeps every message in order.
type recorder struct {
	mu   sync.Mutex
	sent []published
	err  error
}

func (r *recorder) PublishDesired(_ context.Context, device string, body []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, published{device, append([]byte(nil), body...)})
	return nil
}

func (r *recorder) kinds(t *testing.T, device string) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.sent {
		if p.device != device {
			continue
		}
		m, err := ota.DecodeDesiredUpdate(p.body)
		require.NoError(t, err)
		out = append(out, m.Kind)
	}
	return out
}

type updateEnv struct {
	*testEnv
	devices *fakeDevices
	desired *recorder
	clock   time.Time
}

var testPolicy = ota.Policy{
	Acknowledge: time.Hour, Download: 10 * time.Minute, Verify: 5 * time.Minute,
	Install: 30 * time.Minute, Confirm: 20 * time.Minute,
}

func newUpdateEnv(t *testing.T) *updateEnv {
	t.Helper()
	base := newTestEnv(t)
	kp, err := secrets.NewInstanceKeyProvider(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	e := &updateEnv{
		testEnv: base,
		devices: &fakeDevices{profiles: map[string]string{
			"acme/dev-1": "gateway", "acme/dev-2": "gateway", "acme/dev-sensor": "sensor",
			"globex/dev-1": "gateway",
		}},
		desired: &recorder{},
		clock:   time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
	}
	base.api.Secrets = secrets.NewStore(base.api.RDB.Database, kp)
	base.api.Devices = e.devices
	base.api.Desired = e.desired
	base.api.Policy = testPolicy
	base.api.AllowUnverified = true
	base.api.DownloadBaseURL = "https://updates.example.test/api/update-management"
	base.api.CapabilityTTL = 10 * time.Minute
	base.api.Clock = func() time.Time { return e.clock }
	return e
}

// availableArtifact declares an artifact, stores its bytes and marks it AVAILABLE through the real
// completion path.
func (e *updateEnv) availableArtifact(t *testing.T, tenant, token, version string, content []byte) *Artifact {
	t.Helper()
	ctx := tenantCtx(tenant)
	req := validRequest(token, content)
	req.Version = version
	a, err := e.api.CreateArtifact(ctx, req)
	require.NoError(t, err)
	ref, err := e.store.Put(ctx, blobKey(tenant, token), bytes.NewReader(content), blobPutOpts(int64(len(content))))
	require.NoError(t, err)
	require.NoError(t, e.api.CompleteUpload(ctx, a, ref))
	a, err = e.api.ArtifactByToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, StateAvailable, a.State)
	return a
}

func (e *updateEnv) advance(d time.Duration) { e.clock = e.clock.Add(d) }

// progress builds a report for the attempt in view.
func progress(v *UpdateAssignmentView, seq uint64, stage ota.ReportStage, boot string) ota.Report {
	return ota.Report{V: 1, Kind: ota.KindProgress, AttemptID: v.Attempt.Token, AssignmentID: v.Assignment.Token,
		Component: v.Assignment.Component, ArtifactDigest: v.Attempt.TargetDigest, Seq: seq, Stage: stage, BootID: boot}
}

func running(v *UpdateAssignmentView, seq uint64, boot string) ota.Report {
	r := progress(v, seq, ota.StageRunning, boot)
	r.Running = &ota.Running{Version: v.Attempt.TargetVersion, Digest: v.Attempt.TargetDigest}
	return r
}

func (e *updateEnv) mustApply(t *testing.T, tenant, device string, r ota.Report, want ota.Verdict) {
	t.Helper()
	got, err := e.api.ApplyReport(tenantCtx(tenant), device, r)
	require.NoError(t, err)
	require.Equal(t, want, got, "seq %d stage %s", r.Seq, r.Stage)
}

func (e *updateEnv) state(t *testing.T, tenant string, v *UpdateAssignmentView) ota.State {
	t.Helper()
	got, err := e.api.AssignmentByToken(tenantCtx(tenant), v.Assignment.Token)
	require.NoError(t, err)
	require.NotNil(t, got)
	return ota.State(got.Attempt.State)
}

func TestAssignCreatesAQueuedAttemptAndTellsTheDevice(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))

	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)
	require.Equal(t, string(ota.StateQueued), v.Attempt.State)
	require.Equal(t, "dev-1", v.Assignment.DeviceToken)
	require.Equal(t, "alice", v.Assignment.Actor)
	require.True(t, v.Attempt.RequiresReboot, "firmware is installed by rebooting into the new image")

	require.Len(t, e.desired.sent, 1)
	m, err := ota.DecodeDesiredUpdate(e.desired.sent[0].body)
	require.NoError(t, err)
	require.Equal(t, "dev-1", e.desired.sent[0].device)
	require.Equal(t, ota.TrustUnverified, m.Assignment.Trust)
	require.Equal(t, v.Attempt.Token, m.Assignment.AttemptID)
	require.Equal(t, v.Assignment.Token, m.Assignment.AssignmentID)
	require.Equal(t, v.Artifact.Digest, m.Assignment.Artifact.Digest)
	require.EqualValues(t, len("firmware one"), m.Assignment.Artifact.Size)
}

func TestAssignRefusals(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	uploading, err := e.api.CreateArtifact(tenantCtx("acme"), func() *ArtifactCreateRequest {
		r := validRequest("fw-up", []byte("never uploaded"))
		r.Version = "9"
		return r
	}())
	require.NoError(t, err)
	require.Equal(t, StateUploading, uploading.State)

	// An artifact that is not AVAILABLE cannot be assigned.
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-up")
	requireRefusal(t, err, CodeArtifactNotAvailable)

	// A device on a profile the artifact does not list.
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "dev-sensor", "fw-1")
	requireRefusal(t, err, CodeIncompatibleProfile)

	// A device the tenant does not have.
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "ghost", "fw-1")
	requireRefusal(t, err, CodeDeviceNotFound)

	// A device lookup that fails is a refusal, never "compatible".
	e.devices.err = errors.New("device-management is down")
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	requireRefusal(t, err, CodeDeviceLookupUnavailable)
	e.devices.err = nil

	// Another tenant's artifact does not exist for this tenant.
	_, err = e.api.AssignArtifact(tenantCtx("globex"), "dev-1", "fw-1")
	requireRefusal(t, err, CodeArtifactNotFound)

	// UNVERIFIED is refused unless the instance allows it.
	e.api.AllowUnverified = false
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	requireRefusal(t, err, CodeUnverifiedRefused)
	e.api.AllowUnverified = true

	// A retired artifact is withdrawn from new use.
	_, err = e.api.RetireArtifact(tenantCtx("acme"), "fw-1")
	require.NoError(t, err)
	_, err = e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	requireRefusal(t, err, CodeArtifactNotAvailable)

	require.Empty(t, e.desired.sent, "a refused assignment tells the device nothing")
	var n int64
	require.NoError(t, e.api.RDB.Database.Raw("SELECT COUNT(*) FROM update_assignments").Scan(&n).Error)
	require.Zero(t, n, "a refused assignment writes nothing")
}

func TestAssignmentSupersedesOnlyAFinishedAttemptAndKeepsHistory(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	ctx := tenantCtx("acme")

	first, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)

	// The same artifact again is the same assignment.
	again, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	require.Equal(t, first.Assignment.Token, again.Assignment.Token)
	require.Equal(t, first.Attempt.Token, again.Attempt.Token)

	// A different artifact while the attempt is live is refused.
	_, err = e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	requireRefusal(t, err, CodeUpdateInProgress)

	// Cancelled, the slot is free; the old row is kept, stamped superseded.
	_, err = e.api.CancelUpdate(ctx, first.Assignment.Token)
	require.NoError(t, err)
	second, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	require.NoError(t, err)
	require.NotEqual(t, first.Assignment.Token, second.Assignment.Token)

	res, err := e.api.UpdateAssignments(ctx, UpdateAssignmentCriteria{DeviceToken: ptr("dev-1")})
	require.NoError(t, err)
	require.Len(t, res.Results, 2, "history is kept")
	cur, err := e.api.UpdateAssignments(ctx, UpdateAssignmentCriteria{DeviceToken: ptr("dev-1"), CurrentOnly: true})
	require.NoError(t, err)
	require.Len(t, cur.Results, 1)
	require.Equal(t, second.Assignment.Token, cur.Results[0].Assignment.Token)

	// Another tenant sees none of it.
	other, err := e.api.UpdateAssignments(tenantCtx("globex"), UpdateAssignmentCriteria{})
	require.NoError(t, err)
	require.Empty(t, other.Results)
}

func ptr[T any](v T) *T { return &v }

// Every state the reducer defines, reached through the persisted row, and the full happy path ends
// UPDATED only on the confirmation evidence.
func TestTheHappyPathEndsUpdatedOnlyOnTheInstallBootEvidence(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)

	seq := uint64(0)
	next := func(stage ota.ReportStage, boot string) ota.Report { seq++; return progress(v, seq, stage, boot) }
	e.mustApply(t, "acme", "dev-1", next(ota.StageReceived, "boot-1"), ota.VerdictApplied)
	require.Equal(t, ota.StateInitiated, e.state(t, "acme", v))
	d := next(ota.StageDownloading, "boot-1")
	d.Progress = &ota.Progress{Bytes: 5, Total: 12}
	e.mustApply(t, "acme", "dev-1", d, ota.VerdictApplied)
	got, _ := e.api.AssignmentByToken(tenantCtx("acme"), v.Assignment.Token)
	require.EqualValues(t, 5, got.Attempt.ProgressBytes.Int64)
	require.EqualValues(t, 12, got.Attempt.ProgressTotal.Int64)

	// A power cycle while downloading must not satisfy the reboot the update needs.
	e.mustApply(t, "acme", "dev-1", next(ota.StageDownloaded, "boot-2"), ota.VerdictApplied)
	e.mustApply(t, "acme", "dev-1", next(ota.StageVerified, "boot-2"), ota.VerdictApplied)
	e.mustApply(t, "acme", "dev-1", next(ota.StageInstalling, "boot-2"), ota.VerdictApplied)
	e.mustApply(t, "acme", "dev-1", next(ota.StageRebooting, "boot-2"), ota.VerdictApplied)
	require.Equal(t, ota.StateRebooting, e.state(t, "acme", v))

	// RUNNING on the install boot is not evidence: nothing rebooted.
	e.mustApply(t, "acme", "dev-1", running(v, seq+1, "boot-2"), ota.VerdictUnconfirmed)
	require.Equal(t, ota.StateRebooting, e.state(t, "acme", v))
	e.mustApply(t, "acme", "dev-1", running(v, seq+1, "boot-3"), ota.VerdictApplied)
	require.Equal(t, ota.StateUpdated, e.state(t, "acme", v))
	got, _ = e.api.AssignmentByToken(tenantCtx("acme"), v.Assignment.Token)
	require.Equal(t, "boot-2", got.Attempt.InstallBootId, "the install boot survived the round trip through the row")
}

func TestIllegalTransitionsAreRefusedAndChangeNothing(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)

	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloaded, "boot-1"), ota.VerdictApplied)
	// Backwards.
	e.mustApply(t, "acme", "dev-1", progress(v, 2, ota.StageReceived, "boot-1"), ota.VerdictRegression)
	// The same sequence number again: a retransmission.
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloaded, "boot-1"), ota.VerdictDuplicate)
	// The same sequence number with other content.
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloaded, "boot-forged"), ota.VerdictConflict)
	// A sequence number below the last applied one.
	e.mustApply(t, "acme", "dev-1", progress(v, 0, ota.StageVerified, "boot-1"), ota.VerdictRegression)
	// Another attempt's id.
	stale := progress(v, 3, ota.StageVerified, "boot-1")
	stale.AttemptID = "att-someone-else"
	e.mustApply(t, "acme", "dev-1", stale, ota.VerdictStaleAttempt)
	// Another build's digest.
	wrong := progress(v, 3, ota.StageVerified, "boot-1")
	wrong.ArtifactDigest = digestOf([]byte("another build"))
	e.mustApply(t, "acme", "dev-1", wrong, ota.VerdictDigestMismatch)
	// Success with no evidence is not success.
	bare := progress(v, 3, ota.StageRunning, "boot-2")
	bare.Running = &ota.Running{Version: "0.9.0"}
	e.mustApply(t, "acme", "dev-1", bare, ota.VerdictUnconfirmed)
	// ABANDONED without a cancel request.
	e.mustApply(t, "acme", "dev-1", progress(v, 3, ota.StageAbandoned, "boot-1"), ota.VerdictNotCancellable)

	require.Equal(t, ota.StateDownloaded, e.state(t, "acme", v))
	got, _ := e.api.AssignmentByToken(tenantCtx("acme"), v.Assignment.Token)
	require.EqualValues(t, 1, got.Attempt.LastSeq, "none of the refused reports moved the sequence")

	// A failure is the device's to report, and terminal.
	f := progress(v, 4, ota.StageFailed, "boot-1")
	f.Result = &ota.Result{Code: "DISK_FULL", Detail: "no space"}
	e.mustApply(t, "acme", "dev-1", f, ota.VerdictApplied)
	require.Equal(t, ota.StateFailed, e.state(t, "acme", v))
	e.mustApply(t, "acme", "dev-1", progress(v, 5, ota.StageVerified, "boot-1"), ota.VerdictLate)
	got, _ = e.api.AssignmentByToken(tenantCtx("acme"), v.Assignment.Token)
	require.Equal(t, "DISK_FULL", got.Attempt.FailureCode)
	require.Equal(t, string(ota.StageDownloaded), got.Attempt.FailureStage)
}

func TestAReportCannotReachAnotherDevicesOrTenantsAttempt(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)

	// dev-2 names dev-1's attempt: the device comes from the subject, so it finds nothing.
	e.mustApply(t, "acme", "dev-2", progress(v, 1, ota.StageReceived, "b"), VerdictUnknownAssignment)
	// The same device name in another tenant is another device.
	e.mustApply(t, "globex", "dev-1", progress(v, 1, ota.StageReceived, "b"), VerdictUnknownAssignment)
	require.Equal(t, ota.StateQueued, e.state(t, "acme", v))

	// An inventory only ever reconciles the reporting device's own attempt.
	inv := ota.Report{V: 1, Kind: ota.KindInventory, Component: "firmware", BootID: "b", Running: &ota.Running{Version: "1.0.0"}}
	e.mustApply(t, "acme", "dev-2", inv, VerdictUnknownAssignment)
}

func TestDeadlinesTimeOutBeforeInstallAndLeaveUnknownAfter(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	ctx := tenantCtx("acme")
	stalled, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	installing, err := e.api.AssignArtifact(ctx, "dev-2", "fw-2")
	require.NoError(t, err)

	d := progress(stalled, 1, ota.StageDownloading, "boot-1")
	d.Progress = &ota.Progress{Bytes: 1, Total: 12}
	e.mustApply(t, "acme", "dev-1", d, ota.VerdictApplied)
	e.mustApply(t, "acme", "dev-2", progress(installing, 1, ota.StageInstalling, "boot-1"), ota.VerdictApplied)

	// Nothing is due yet.
	res, err := e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, res.TimedOut+res.Unknown)

	// Eleven minutes of silence: the download deadline (10m) has passed, the install one (30m) not.
	e.advance(11 * time.Minute)
	res, err = e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, res.TimedOut)
	require.Equal(t, ota.StateTimedOut, e.state(t, "acme", stalled))
	require.Equal(t, ota.StateUpdating, e.state(t, "acme", installing))
	require.Equal(t, []string{ota.KindUpdateAssignment, ota.KindUpdateCancel}, e.desired.kinds(t, "dev-1"),
		"a device whose attempt timed out is told to stop")

	// Late progress on a terminal attempt is not applied.
	e.mustApply(t, "acme", "dev-1", progress(stalled, 2, ota.StageDownloaded, "boot-1"), ota.VerdictLate)

	// Silence during installation is UNKNOWN, never failure and never success.
	e.advance(25 * time.Minute)
	res, err = e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, res.Unknown)
	require.Equal(t, ota.StateUnknown, e.state(t, "acme", installing))
	// A second pass changes nothing.
	res, err = e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, res.TimedOut+res.Unknown)

	// UNKNOWN leaves on evidence: an inventory from a rebooted device running the target.
	inv := ota.Report{V: 1, Kind: ota.KindInventory, Component: "firmware", BootID: "boot-9",
		Running: &ota.Running{Version: "2.0.0"}}
	e.mustApply(t, "acme", "dev-2", inv, ota.VerdictApplied)
	require.Equal(t, ota.StateUpdated, e.state(t, "acme", installing))
}

// The sweep must not be crowded out by rows that are old but not yet past their own deadline.
func TestTheSweepSelectsByEachPhasesOwnDeadline(t *testing.T) {
	e := newUpdateEnv(t)
	for _, n := range []string{"1", "2", "3"} {
		e.availableArtifact(t, "acme", "fw-"+n, n+".0.0", []byte("firmware "+n))
	}
	ctx := tenantCtx("acme")
	e.devices.profiles["acme/dev-3"] = "gateway"
	// Two attempts QUEUED (a one-hour deadline) and one DOWNLOADING (ten minutes).
	_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	_, err = e.api.AssignArtifact(ctx, "dev-2", "fw-2")
	require.NoError(t, err)
	dl, err := e.api.AssignArtifact(ctx, "dev-3", "fw-3")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-3", progress(dl, 1, ota.StageDownloading, "b"), ota.VerdictApplied)

	e.advance(20 * time.Minute)
	// A page of one: the QUEUED rows are older than the download row but not yet due, so a query
	// that only looked at age would spend its page on them and never reach the one that is.
	res, err := e.api.SweepDeadlines(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, res.TimedOut)
	require.Equal(t, ota.StateTimedOut, e.state(t, "acme", dl))
}

func TestCancelFollowsTheContract(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")

	// QUEUED: cancelled outright, and the device is told in case it already holds the assignment.
	q, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	out, err := e.api.CancelUpdate(ctx, q.Assignment.Token)
	require.NoError(t, err)
	require.Equal(t, string(ota.StateCancelled), out.Attempt.State)
	require.Equal(t, []string{ota.KindUpdateAssignment, ota.KindUpdateCancel}, e.desired.kinds(t, "dev-1"))
	_, err = e.api.CancelUpdate(ctx, q.Assignment.Token)
	requireRefusal(t, err, CodeAttemptTerminal)

	// Before installation: a request, completed by the device's ABANDONED.
	d, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(d, 1, ota.StageDownloading, "b"), ota.VerdictApplied)
	out, err = e.api.CancelUpdate(ctx, d.Assignment.Token)
	require.NoError(t, err)
	require.Equal(t, string(ota.StateDownloading), out.Attempt.State)
	require.True(t, out.Attempt.CancelRequested)
	e.mustApply(t, "acme", "dev-1", progress(d, 2, ota.StageAbandoned, "b"), ota.VerdictApplied)
	require.Equal(t, ota.StateCancelled, e.state(t, "acme", d))

	// From installation on: beyond recall.
	i, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(i, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	_, err = e.api.CancelUpdate(ctx, i.Assignment.Token)
	requireRefusal(t, err, CodeBeyondRecall)

	// Another tenant cannot cancel it.
	_, err = e.api.CancelUpdate(tenantCtx("globex"), i.Assignment.Token)
	requireRefusal(t, err, CodeAssignmentNotFound)
}

func TestACancelRacingAReportLosesNothing(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloading, "b"), ota.VerdictApplied)

	var wg sync.WaitGroup
	wg.Add(2)
	var cancelErr error
	go func() { defer wg.Done(); _, cancelErr = e.api.CancelUpdate(ctx, v.Assignment.Token) }()
	go func() {
		defer wg.Done()
		_, _ = e.api.ApplyReport(ctx, "dev-1", progress(v, 2, ota.StageDownloaded, "b"))
	}()
	wg.Wait()
	require.NoError(t, cancelErr)
	got, err := e.api.AssignmentByToken(ctx, v.Assignment.Token)
	require.NoError(t, err)
	require.True(t, got.Attempt.CancelRequested, "the cancel request survived the concurrent report")
}

func blobKey(tenant, token string) blob.Key {
	return blob.Key{Tenant: tenant, Purpose: BlobPurpose, ID: token + "-object"}
}

func blobPutOpts(n int64) blob.PutOptions { return blob.PutOptions{MaxSize: n} }
