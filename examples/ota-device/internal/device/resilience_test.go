// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/device"
	"devicechain.io/examples/ota-device/internal/wire"
)

func stagePathOf(e *env) string {
	return filepath.Join(boot.SlotDir(e.root, "b"), "ota-device.stage"+exeSuffix())
}

// seedAttempt writes the attempt.json a device would hold after dying at the given stage, and (when
// stage is non-empty) the bytes it would have staged, then restarts the agent from it.
func seedAttempt(t *testing.T, e *env, a wire.Assignment, stage string, staged []byte) {
	t.Helper()
	m := map[string]any{
		"attemptId": a.AttemptID, "assignmentId": a.AssignmentID, "component": a.Component,
		"version": a.Artifact.Version, "digest": a.Artifact.Digest, "size": a.Artifact.Size,
		"trust": a.Trust, "signature": a.Signature, "keyId": a.KeyID, "algorithm": a.Algorithm,
		"requiresReboot": true, "seq": 3, "stage": stage, "reported": stage,
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(e.root, "attempt.json"), b, 0o600))
	if staged != nil {
		require.NoError(t, os.MkdirAll(boot.SlotDir(e.root, "b"), 0o700))
		require.NoError(t, os.WriteFile(stagePathOf(e), staged, 0o700))
	}
	e.agent = e.newAgent("a", "1.0.0", "boot-2")
}

// Item: a stage is durable before it is reported, so a dead link cannot make the device forget it
// has already installed. The link goes down after DOWNLOADING; the install still happens; after the
// reboot RUNNING reaches the platform, and the image is never downloaded a second time.
func TestLinkDownAfterDownloadingStillInstallsAndReportsRunningLater(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.rec.downAt = `"stage":"DOWNLOADING"`

	e.run(a, "fw-101")
	assert.True(t, e.didReboot(), "the install does not wait for the platform")
	assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState())
	assert.Equal(t, []string{"RECEIVED", "DOWNLOADING"}, e.rec.stages(t), "nothing after DOWNLOADING got out")
	got, err := os.ReadFile(boot.ImagePath(e.root, "b"))
	require.NoError(t, err)
	assert.Equal(t, img.digest, verifyDigest(got))

	runLauncher(t, e, 1)
	e.rec.up()
	e.agent = e.newAgent("b", "1.0.1", "boot-2")
	require.NoError(t, e.agent.OnConnect(context.Background()))

	ps := e.rec.stages(t)
	assert.Equal(t, "RUNNING", ps[len(ps)-1])
	assert.Len(t, e.ranges(), 1, "the image was downloaded once")
	// The platform's reducer is satisfied by what finally arrived: it skipped stages, which is allowed.
	final, verdicts := foldPlatform(t, e.rec, img)
	for _, v := range verdicts {
		assert.Equal(t, ota.VerdictApplied, v)
	}
	assert.Equal(t, ota.StateUpdated, final.State)
}

// A restart in the middle of the attempt, at each point it can die.
func TestResumeAtEveryStage(t *testing.T) {
	img := mkImage(t, "fw-101", "1.0.1", product, "good")

	t.Run("DOWNLOADED with the whole file staged installs without downloading", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "DOWNLOADED", img.bytes)
		e.deliver(a) // the platform resends after the device's sync
		assert.True(t, e.didReboot())
		assert.Empty(t, e.ranges())
		assert.Zero(t, e.rec.count(t, wire.KindDownloadRequest))
		ps := e.rec.stages(t)
		assert.Equal(t, "REBOOTING", ps[len(ps)-1])
	})
	t.Run("VERIFIED with the whole file staged installs without downloading", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "VERIFIED", img.bytes)
		e.deliver(a)
		assert.True(t, e.didReboot())
		assert.Empty(t, e.ranges())
	})
	t.Run("DOWNLOADED but the staged file is gone goes back to downloading", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "DOWNLOADED", nil)
		e.deliver(a)
		assert.Equal(t, 1, e.rec.count(t, wire.KindDownloadRequest))
		e.deliver(e.grantFor(a, "fw-101")) // the grant is not dropped
		assert.True(t, e.didReboot())
	})
	t.Run("a staged file of the wrong size is not trusted", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "DOWNLOADED", img.bytes[:100])
		e.deliver(a)
		assert.False(t, e.didReboot())
		assert.Equal(t, 1, e.rec.count(t, wire.KindDownloadRequest))
	})
	t.Run("an attempt whose RECEIVED was never made durable is accepted again", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "", nil)
		e.deliver(a)
		assert.Equal(t, []string{"RECEIVED"}, e.rec.stages(t))
		assert.Equal(t, 1, e.rec.count(t, wire.KindDownloadRequest))
		e.deliver(e.grantFor(a, "fw-101"))
		assert.True(t, e.didReboot(), "it is not stuck")
	})
	t.Run("policy is applied again to a resumed attempt", func(t *testing.T) {
		e := newEnv(t)
		a := e.publish(img, img.bytes, e.signer, nil)
		seedAttempt(t, e, a, "DOWNLOADED", img.bytes)
		e.agent = e.newAgent("a", "1.0.1", "boot-3") // the device has meanwhile come to run 1.0.1
		e.deliver(a)
		code, _, ok := e.rec.failure(t)
		require.True(t, ok)
		assert.Equal(t, "DOWNGRADE_REFUSED", code)
		assert.False(t, e.didReboot())
		assert.NoFileExists(t, stagePathOf(e))
	})
}

// A shutdown during the preflight is not a verdict on the image.
func TestShutdownDuringPreflightIsNotAnImageMismatch(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	seedAttempt(t, e, a, "VERIFIED", img.bytes)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.agent.OnAssignment(ctx, a)
	_, _, failed := e.rec.failure(t)
	assert.False(t, failed)
	assert.FileExists(t, stagePathOf(e), "the staged image is kept for the next start")
	assert.False(t, e.didReboot())
}

func TestSignedAssignmentWithNoTrustSetIsRefusedNotAPanic(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	// a lab device: unsigned allowed, and no trust set at all
	d, err := device.New(device.Config{
		Root: e.root, Slot: "a", Component: "firmware", Product: product, Version: "1.0.0", BootID: "boot-9",
		RequireSigned: false, Origin: e.origin(), AllowInsecure: true, HTTP: e.srv.Client(),
		SelfPath: boot.ImagePath(e.root, "a"), ProgressEvery: time.Hour,
	}, e.rec)
	require.NoError(t, err)
	e.agent = d
	e.run(a, "fw-101")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "UNKNOWN_SIGNER", code)
	assert.Equal(t, "", e.marker("b"))
	untouched(t, e)
}

func TestForeignComponentDoesNotDisturbTheAttemptInFlight(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.cutFirst["fw-101"] = len(img.bytes) / 2
	e.deliver(a)
	e.deliver(e.grantFor(a, "fw-101")) // leaves a partial file
	part := filepath.Join(boot.SlotDir(e.root, "b"), "ota-device.part"+exeSuffix())
	fi, err := os.Stat(part)
	require.NoError(t, err)
	before := len(e.rec.all())

	other := e.publish(img, img.bytes, e.signer, func(x *wire.Assignment) { x.Component = "bootloader" })
	e.deliver(other)

	frames := e.rec.all()[before:]
	require.Len(t, frames, 1, "one standalone answer")
	var r wire.Report
	require.NoError(t, json.Unmarshal(frames[0], &r))
	assert.Equal(t, other.AttemptID, r.AttemptID, "under its own ids")
	assert.Equal(t, uint64(1), r.Seq)
	assert.Equal(t, "FAILED", r.Stage)
	assert.Equal(t, "WRONG_COMPONENT", r.Result.Code)
	fi2, err := os.Stat(part)
	require.NoError(t, err, "the download in flight keeps its bytes")
	assert.Equal(t, fi.Size(), fi2.Size())

	// and the real attempt still completes
	e.deliver(e.grantFor(a, "fw-101"))
	assert.True(t, e.didReboot())
}

func TestInstallRefusedUnlessRunningTheConfirmedSlot(t *testing.T) {
	e := newEnv(t)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	require.NoError(t, boot.Stage(e.root, "b")) // something else is already pending
	e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
	code, _, ok := e.rec.failure(t)
	require.True(t, ok)
	assert.Equal(t, "INSTALL_FAILED", code)
	assert.Empty(t, e.ranges())
	assert.False(t, e.didReboot())
}

// A slot is confirmed only after the platform has been told, and only if what runs is what was installed.
func TestConfirmWaitsForTheSyncAndTheEvidence(t *testing.T) {
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	t.Run("sync that cannot be sent leaves the slot pending", func(t *testing.T) {
		e := newEnv(t)
		e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
		e.agent = e.newAgent("b", "1.0.1", "boot-2")
		e.rec.failOn, e.rec.failN = `"kind":"sync"`, 1
		require.Error(t, e.agent.OnConnect(context.Background()))
		assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState())
		require.NoError(t, e.agent.OnConnect(context.Background()))
		assert.Equal(t, boot.State{Confirmed: "b"}, e.bootState())
	})
	t.Run("right version, wrong file is not confirmed", func(t *testing.T) {
		e := newEnv(t)
		e.run(e.publish(img, img.bytes, e.signer, nil), "fw-101")
		e.installImage("b", mkImage(t, "other", "1.0.1", product, "hang").bytes)
		e.agent = e.newAgent("b", "1.0.1", "boot-2")
		require.NoError(t, e.agent.OnConnect(context.Background()))
		assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState(), "the launcher will roll it back")
	})
}

// ---------------------------------------------------------------------------------------------
// The Async:true path the real binary uses, and a download that is superseded while running.
// ---------------------------------------------------------------------------------------------

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	require.Eventually(t, cond, 30*time.Second, 20*time.Millisecond, what)
}

func TestAsyncGrantInstalls(t *testing.T) {
	e := newEnvOpt(t, true)
	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	a := e.publish(img, img.bytes, e.signer, nil)
	e.deliver(a)
	e.deliver(e.grantFor(a, "fw-101")) // returns at once; the download runs on its own goroutine
	eventually(t, e.didReboot, "the async install finishes")
	assert.Equal(t, []string{"RECEIVED", "DOWNLOADING", "DOWNLOADED", "VERIFIED", "INSTALLING", "REBOOTING"}, e.rec.stages(t))
}

// A download that is replaced mid-flight must not reset the busy flag or the cancel func of the
// attempt that replaced it, and must not report anything for itself.
func TestSupersededDownloadDoesNotDisturbItsReplacement(t *testing.T) {
	e := newEnvOpt(t, true)
	slow := mkImage(t, "fw-slow", "1.0.1", product, "good")
	fast := mkImage(t, "fw-fast", "1.0.2", product, "good")
	release := make(chan struct{})
	e.hold["fw-slow"] = release

	a1 := e.publish(slow, slow.bytes, e.signer, nil)
	e.deliver(a1)
	e.deliver(e.grantFor(a1, "fw-slow"))
	eventually(t, func() bool { return len(e.ranges()) == 1 }, "the first download is under way")

	a2 := e.publish(fast, fast.bytes, e.signer, nil) // supersedes it
	e.deliver(a2)
	e.deliver(e.grantFor(a2, "fw-fast"))
	eventually(t, e.didReboot, "the replacement installs")
	close(release)
	time.Sleep(200 * time.Millisecond) // give a stray goroutine the chance to misbehave

	got, err := os.ReadFile(boot.ImagePath(e.root, "b"))
	require.NoError(t, err)
	assert.Equal(t, fast.digest, verifyDigest(got), "slot b holds the replacement, not the superseded image")
	for _, p := range e.rec.progress(t) {
		if p.AttemptID == a1.AttemptID {
			assert.NotContains(t, []string{"DOWNLOADED", "VERIFIED", "INSTALLING", "REBOOTING", "RUNNING"}, string(p.Stage))
		}
	}
	assert.Equal(t, boot.State{Confirmed: "a", Pending: "b"}, e.bootState())
}

// ---------------------------------------------------------------------------------------------
// The periodic grant re-request.
// ---------------------------------------------------------------------------------------------

func TestTickReRequestsAGrantWithBoundedJitteredBackoff(t *testing.T) {
	e := newEnv(t)
	var now atomic.Pointer[time.Time]
	set := func(d time.Duration) { v := time.Unix(1_800_000_000, 0).Add(d); now.Store(&v) }
	set(0)
	d, err := device.New(device.Config{
		Root: e.root, Slot: "a", Component: "firmware", Product: product, Version: "1.0.0", BootID: "boot-1",
		Trust: e.trust, RequireSigned: true, Origin: e.origin(), AllowInsecure: true, HTTP: e.srv.Client(),
		SelfPath: boot.ImagePath(e.root, "a"), ProgressEvery: time.Hour,
		Now: func() time.Time { return *now.Load() }, Jitter: func() float64 { return 0.5 }, // no jitter
		AskMin: time.Minute, AskMax: 5 * time.Minute,
	}, e.rec)
	require.NoError(t, err)
	e.agent = d

	img := mkImage(t, "fw-101", "1.0.1", product, "good")
	e.deliver(e.publish(img, img.bytes, e.signer, nil))
	asks := func() int { return e.rec.count(t, wire.KindDownloadRequest) }
	require.Equal(t, 1, asks())

	ctx := context.Background()
	set(59 * time.Second)
	d.Tick(ctx)
	assert.Equal(t, 1, asks(), "not before the first minute")
	set(61 * time.Second)
	d.Tick(ctx)
	assert.Equal(t, 2, asks())
	set(61*time.Second + 119*time.Second)
	d.Tick(ctx)
	assert.Equal(t, 2, asks(), "the next wait is two minutes")
	set(61*time.Second + 121*time.Second)
	d.Tick(ctx)
	assert.Equal(t, 3, asks())
	// doubling is capped at five minutes
	last := 61*time.Second + 121*time.Second
	for _, want := range []time.Duration{4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		set(last + want - time.Second)
		before := asks()
		d.Tick(ctx)
		assert.Equal(t, before, asks(), "still waiting %s", want)
		last += want + time.Second
		set(last)
		d.Tick(ctx)
		assert.Equal(t, before+1, asks(), "asked after %s", want)
	}
}

func TestTickDoesNothingWithoutAnAttemptWaitingForBytes(t *testing.T) {
	e := newEnv(t)
	e.agent.Tick(context.Background())
	assert.Empty(t, e.rec.all())
}
