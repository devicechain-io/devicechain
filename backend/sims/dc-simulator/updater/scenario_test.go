// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/internal/platformtest"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

// stages lists the stage of every progress frame the link delivered, in order.
func stages(l *platformtest.Link) []string {
	var out []string
	for _, f := range l.Decoded() {
		if s, ok := f["stage"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// seqs lists the seq of every progress frame delivered, in order.
func seqs(l *platformtest.Link) []uint64 {
	var out []uint64
	for _, f := range l.Decoded() {
		if s, ok := f["seq"].(float64); ok {
			out = append(out, uint64(s))
		}
	}
	return out
}

// requireEveryFrameDecodes is the cross-check that the simulator's own structs produce what the
// contract's strict decoder accepts.
func requireEveryFrameDecodes(t *testing.T, l *platformtest.Link) {
	t.Helper()
	require.NotEmpty(t, l.Frames())
	for i, f := range l.Frames() {
		_, err := ota.DecodeReport(f)
		require.NoError(t, err, "frame %d: %s", i, f)
	}
}

func TestHappyPathWithReboot(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.runToEnd()

	a := r.platform.Attempt()
	assert.Equal(t, ota.StateUpdated, a.State)
	assert.False(t, a.Reconciled)
	want := []string{"RECEIVED"}
	for i := 0; i < imageSize/chunk; i++ {
		want = append(want, "DOWNLOADING")
	}
	want = append(want, "DOWNLOADED", "VERIFIED", "INSTALLING", "REBOOTING", "RUNNING")
	assert.Equal(t, want, stages(r.link))
	for i, s := range seqs(r.link) {
		assert.Equal(t, uint64(i+1), s, "seq must be consecutive from 1")
	}
	assert.Equal(t, len(r.link.Frames()), r.platform.Count(ota.VerdictApplied), "every frame applied, none rejected")
	requireEveryFrameDecodes(t, r.link)

	// The confirmation is from a different boot than the install.
	frames := r.link.Decoded()
	assert.NotEqual(t, frames[len(frames)-2]["bootId"], frames[len(frames)-1]["bootId"])
}

func TestHappyPathWithoutReboot(t *testing.T) {
	r := newRig(t, false)
	r.assign()
	r.runToEnd()

	assert.Equal(t, ota.StateUpdated, r.state())
	assert.NotContains(t, stages(r.link), "REBOOTING")
	requireEveryFrameDecodes(t, r.link)
}

// Kill the updater at 40% of the download, restart it over the same state file: the same attempt
// continues, the sequence carries on, and the bytes already held are not fetched again.
func TestResumeAfterRestartContinuesSameAttempt(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.up.SetFaults(updater.Faults{Crash: func(p updater.CrashPoint, s updater.Snapshot) bool {
		return p == updater.CrashBeforeSend && s.Stage == "DOWNLOADING" && s.BytesHave >= 4*chunk
	}})
	_, _, err := r.up.Run(ctx, 200)
	require.ErrorIs(t, err, updater.ErrCrashed)
	_, err = r.up.Step(ctx)
	require.ErrorIs(t, err, updater.ErrCrashed, "a dead instance does nothing further")

	delivered := len(r.link.Frames())
	r.restart()
	snap := r.up.Snapshot()
	assert.Equal(t, "att-1", snap.AttemptID)
	assert.Equal(t, int64(4*chunk), snap.BytesHave)
	assert.True(t, snap.Pending, "the report decided before the crash is still owed")
	persistedSeq := snap.Seq

	owed := pendingBytes(t, r.cfg.StatePath)
	require.NotEmpty(t, owed)

	r.src.resetReads()
	r.runToEnd()

	assert.Equal(t, string(owed), string(r.link.Frames()[delivered]), "the owed report is resent byte for byte")
	after := r.link.Decoded()[delivered:]
	require.NotEmpty(t, after)
	assert.Equal(t, "att-1", after[0]["attemptId"])
	assert.EqualValues(t, persistedSeq, after[0]["seq"], "the owed report is resent, not renumbered")
	assert.EqualValues(t, 4*chunk, after[0]["progress"].(map[string]any)["bytes"])
	assert.GreaterOrEqual(t, r.src.minOffset(), int64(4*chunk), "no byte already held is fetched again")

	assert.Equal(t, ota.StateUpdated, r.state())
	for i, s := range seqs(r.link) {
		assert.Equal(t, uint64(i+1), s, "no gap and no repeat across the restart")
	}
	assert.Zero(t, r.platform.Count(ota.VerdictRegression)+r.platform.Count(ota.VerdictConflict)+r.platform.Count(ota.VerdictDuplicate))
}

// A crash after the send but before the device recorded it makes the restarted device repeat a
// report the platform already has; the platform must call that a duplicate and carry on.
func TestRestartAfterSendRepeatsAsDuplicate(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.up.SetFaults(updater.Faults{Crash: func(p updater.CrashPoint, s updater.Snapshot) bool {
		return p == updater.CrashAfterSend && s.Stage == "DOWNLOADING" && s.BytesHave >= 4*chunk
	}})
	_, _, err := r.up.Run(ctx, 200)
	require.ErrorIs(t, err, updater.ErrCrashed)
	before := r.platform.Attempt()

	r.restart()
	require.True(t, r.up.Snapshot().Pending)
	_, err = r.up.Step(ctx) // resends the report the platform already holds
	require.NoError(t, err)
	assert.Equal(t, 1, r.platform.Count(ota.VerdictDuplicate))
	assert.Equal(t, before, r.platform.Attempt(), "a duplicate changes nothing")

	r.runToEnd()
	assert.Equal(t, ota.StateUpdated, r.state())
}

// A dropped link makes steps fail without losing ground; when it returns the same report goes out
// and the update completes with no gap in the sequence.
func TestResumeAfterReconnect(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 4; i++ { // RECEIVED + 3 chunks
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	before := r.up.Snapshot()
	r.link.SetDown(true)
	for i := 0; i < 3; i++ {
		_, err := r.up.Step(ctx)
		require.ErrorIs(t, err, platformtest.ErrLinkDown)
	}
	during := r.up.Snapshot()
	assert.True(t, during.Pending)
	assert.Equal(t, before.Seq+1, during.Seq, "retrying does not consume sequence numbers")

	r.link.SetDown(false)
	r.runToEnd()
	assert.Equal(t, ota.StateUpdated, r.state())
	for i, s := range seqs(r.link) {
		assert.Equal(t, uint64(i+1), s)
	}
	assert.Zero(t, r.platform.Count(ota.VerdictDuplicate), "nothing reached the platform while the link was down")
}

// A failing source is a download that stalls: no progress is recorded, and the same chunk is asked
// for again.
func TestSourceDisconnectMidDownloadRetriesTheSameChunk(t *testing.T) {
	r := newRig(t, false)
	r.assign()
	for i := 0; i < 4; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	have := r.up.Snapshot().BytesHave
	r.src.setErr(errors.New("connection reset"))
	_, err := r.up.Step(ctx)
	require.ErrorContains(t, err, "connection reset")
	assert.Equal(t, have, r.up.Snapshot().BytesHave)

	r.src.setErr(nil)
	r.runToEnd()
	assert.Equal(t, ota.StateUpdated, r.state())
}

// A report whose acknowledgement was lost is resent, and the platform treats the repeat as a
// duplicate.
func TestLostAckIsResentAndIdempotent(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 3; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	r.link.LoseNextAcks(1)
	_, err := r.up.Step(ctx)
	require.ErrorIs(t, err, platformtest.ErrAckLost)
	r.runToEnd()

	assert.Equal(t, 1, r.platform.Count(ota.VerdictDuplicate))
	assert.Equal(t, ota.StateUpdated, r.state())
}

// An injected duplicate leaves the platform exactly where a run without it ends up.
func TestInjectedDuplicateIsIdempotent(t *testing.T) {
	control := newRig(t, true)
	control.assign()
	control.runToEnd()

	r := newRig(t, true)
	r.assign()
	for i := 0; i < 5; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	r.up.SetFaults(updater.Faults{DuplicateNext: true})
	r.runToEnd()

	assert.Equal(t, 1, r.platform.Count(ota.VerdictDuplicate))
	assert.Equal(t, control.platform.Attempt(), r.platform.Attempt())
}

func TestFailedVerificationFailsTheAttempt(t *testing.T) {
	t.Run("injected", func(t *testing.T) {
		r := newRig(t, true)
		r.assign()
		r.up.SetFaults(updater.Faults{FailVerification: true})
		r.runToEnd()

		a := r.platform.Attempt()
		assert.Equal(t, ota.StateFailed, a.State)
		require.NotNil(t, a.Failure)
		assert.Equal(t, "VERIFICATION_FAILED", a.Failure.Code)
		assert.False(t, a.Failure.Platform, "the device said it, not the platform")
		assert.NotContains(t, stages(r.link), "VERIFIED")
		assert.NotContains(t, stages(r.link), "INSTALLING")
		requireEveryFrameDecodes(t, r.link)
	})
	t.Run("corrupt bytes", func(t *testing.T) {
		r := newRig(t, true)
		r.src.data[5*chunk+17] ^= 0xff
		r.assign()
		r.runToEnd()

		a := r.platform.Attempt()
		assert.Equal(t, ota.StateFailed, a.State)
		require.NotNil(t, a.Failure)
		assert.Contains(t, a.Failure.Detail, "digest mismatch")
		assert.NotContains(t, stages(r.link), "INSTALLING")
	})
}

// The device boots the new image and says nothing. The platform must not call that success: it
// waits out the deadline and marks the attempt UNKNOWN, and only evidence resolves it.
func TestRebootWithoutConfirmationEndsUnknown(t *testing.T) {
	arrange := func(t *testing.T, f updater.Faults) *rig {
		r := newRig(t, true)
		r.assign()
		f.NoConfirmAfterReboot = true
		r.up.SetFaults(f)
		r.runToEnd()
		assert.Equal(t, ota.StateRebooting, r.state(), "the device is silent after REBOOTING")
		assert.NotContains(t, stages(r.link), "RUNNING")

		r.clock.Advance(testPolicy.Confirm - time.Second)
		changed, err := r.platform.Tick()
		require.NoError(t, err)
		assert.False(t, changed, "within the deadline nothing moves")
		r.clock.Advance(2 * time.Second)
		changed, err = r.platform.Tick()
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, ota.StateUnknown, r.state())
		return r
	}

	t.Run("inventory shows the new version on a new boot", func(t *testing.T) {
		r := arrange(t, updater.Faults{})
		require.NoError(t, r.up.ReportInventory(ctx))
		a := r.platform.Attempt()
		assert.Equal(t, ota.StateUpdated, a.State)
		assert.True(t, a.Reconciled, "reached UPDATED from UNKNOWN by evidence")
	})
	t.Run("inventory shows the old image came back", func(t *testing.T) {
		r := arrange(t, updater.Faults{BootOldImage: true})
		require.NoError(t, r.up.ReportInventory(ctx))
		a := r.platform.Attempt()
		assert.Equal(t, ota.StateFailed, a.State)
		require.NotNil(t, a.Failure)
		assert.Equal(t, "NOT_RUNNING_TARGET", a.Failure.Code)
		assert.True(t, a.Failure.Platform)
	})
	t.Run("the device finally confirms", func(t *testing.T) {
		r := arrange(t, updater.Faults{})
		r.up.SetFaults(updater.Faults{})
		r.runToEnd()
		a := r.platform.Attempt()
		assert.Equal(t, ota.StateUpdated, a.State)
		assert.True(t, a.Reconciled)
	})
	t.Run("a silent device never becomes UPDATED by waiting", func(t *testing.T) {
		r := arrange(t, updater.Faults{})
		r.clock.Advance(365 * 24 * time.Hour)
		_, err := r.platform.Tick()
		require.NoError(t, err)
		assert.Equal(t, ota.StateUnknown, r.state())
	})
}

func TestRollbackIsReportedAsFailure(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.up.SetFaults(updater.Faults{BootOldImage: true})
	r.runToEnd()

	a := r.platform.Attempt()
	assert.Equal(t, ota.StateFailed, a.State)
	require.NotNil(t, a.Failure)
	assert.Equal(t, "ROLLED_BACK", a.Failure.Code)
	assert.Equal(t, "1.0.0", r.up.Snapshot().RunningVersion)
}

// A report from an attempt the platform has moved past is counted and never applied.
func TestStaleAttemptReplay(t *testing.T) {
	assign2 := func(r *rig) {
		r.asg.AttemptID, r.asg.AssignmentID, r.asg.Version = "att-2", "asg-2", "3.0.0"
		require.NoError(t, r.up.Assign(r.asg))
	}
	finish := func(t *testing.T) *rig {
		r := newRig(t, true)
		r.assign()
		r.runToEnd()
		require.Equal(t, ota.StateUpdated, r.state())
		return r
	}

	t.Run("against the next attempt", func(t *testing.T) {
		r := finish(t)
		assign2(r)
		att2, err := ota.NewAttempt("att-2", "asg-2", "firmware",
			ota.Target{Version: "3.0.0", Digest: r.asg.ArtifactDigest, RequiresReboot: true}, r.clock.Now())
		require.NoError(t, err)
		p2 := platformtest.New(r.clock, testPolicy, att2)
		r.cfg.Transport = platformtest.NewLink(p2)
		r.restart()

		require.NoError(t, r.up.ReplayPrevious(ctx))
		assert.Equal(t, 1, p2.Count(ota.VerdictStaleAttempt))
		assert.Equal(t, att2, p2.Attempt(), "the stale report applied nothing")
	})
	t.Run("against the attempt it belongs to", func(t *testing.T) {
		r := finish(t)
		assign2(r)
		before := r.platform.Attempt()
		require.NoError(t, r.up.ReplayPrevious(ctx))
		assert.Equal(t, 1, r.platform.Count(ota.VerdictDuplicate))
		assert.Equal(t, before, r.platform.Attempt())
	})
	t.Run("an old progress report after the attempt ended", func(t *testing.T) {
		r := finish(t)
		require.NoError(t, r.link.Send(ctx, r.link.Frames()[3]))
		assert.Equal(t, 1, r.platform.Count(ota.VerdictLate))
		assert.Equal(t, ota.StateUpdated, r.state())
	})
}

// A device that stalls past its deadline is timed out; if it later carries on, its progress is
// late, not applied, and the attempt never reaches UPDATED.
func TestProgressAfterTimeoutIsLate(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 4; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	r.link.SetDown(true)
	r.clock.Advance(testPolicy.Download + time.Second)
	changed, err := r.platform.Tick()
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, ota.StateTimedOut, r.state())

	r.link.SetDown(false)
	r.runToEnd()
	assert.Equal(t, ota.StateTimedOut, r.state())
	assert.Positive(t, r.platform.Count(ota.VerdictLate))
	assert.NotEqual(t, ota.StateUpdated, r.state())
}

func TestCancelBeforeInstallAbandons(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 4; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, r.platform.RequestCancel())
	require.NoError(t, r.up.Abandon(ctx))

	assert.Equal(t, ota.StateCancelled, r.state())
	done, err := r.up.Step(ctx)
	require.NoError(t, err)
	assert.True(t, done, "an abandoned attempt does nothing further")
	assert.ErrorIs(t, r.up.Abandon(ctx), updater.ErrFinished)
}

func TestCancelAfterInstallIsBeyondRecall(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for r.up.Snapshot().Stage != "INSTALLING" {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	assert.ErrorIs(t, r.platform.RequestCancel(), ota.ErrBeyondRecall)
	assert.ErrorIs(t, r.up.Abandon(ctx), updater.ErrPastRecall)
}

func TestAssignRules(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	require.NoError(t, r.up.Assign(r.asg), "re-delivery of the same assignment is a no-op")

	other := r.asg
	other.AttemptID = "att-9"
	assert.ErrorIs(t, r.up.Assign(other), updater.ErrAttemptInProgress)

	changed := r.asg
	changed.Version = "9.9.9"
	assert.ErrorContains(t, r.up.Assign(changed), "different content")

	wrong := r.asg
	wrong.AttemptID, wrong.Component = "att-10", "bootloader"
	assert.ErrorContains(t, r.up.Assign(wrong), "component")

	zero := r.asg
	zero.AttemptID, zero.Size = "att-11", 0
	assert.Error(t, r.up.Assign(zero))
}

// pendingBytes reads the report the state file says is owed.
func pendingBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var f struct {
		Attempt struct {
			Pending json.RawMessage `json:"pending"`
		} `json:"attempt"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	return f.Attempt.Pending
}

// A restart between the boot and the confirmation must not boot again: the device is already
// running the new image, and a second boot would change the boot id the platform compares.
func TestRestartAfterRebootDoesNotRebootTwice(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.up.SetFaults(updater.Faults{NoConfirmAfterReboot: true})
	r.runToEnd()
	booted := r.up.Snapshot()
	require.Equal(t, "REBOOTING", booted.Stage)
	require.Equal(t, "boot-2", booted.BootID)

	r.restart() // no faults now: the device goes on to confirm
	r.runToEnd()

	assert.Equal(t, "boot-2", r.up.Snapshot().BootID, "no second boot")
	frames := r.link.Decoded()
	assert.Equal(t, "boot-2", frames[len(frames)-1]["bootId"])
	assert.Equal(t, ota.StateUpdated, r.state())
}

// Same sequence number, different content: the platform calls it a conflict and applies nothing.
func TestSameSeqWithDifferentContentIsAConflict(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	for i := 0; i < 3; i++ {
		_, err := r.up.Step(ctx)
		require.NoError(t, err)
	}
	r.up.SetFaults(updater.Faults{ConflictNext: true})
	_, err := r.up.Step(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, r.platform.Count(ota.VerdictConflict))
	fr := r.link.Decoded()
	assert.Equal(t, fr[len(fr)-2]["seq"], fr[len(fr)-1]["seq"])
	r.runToEnd()
	assert.Equal(t, ota.StateUpdated, r.state(), "the original report stood")
}

// An attempt that has ended but whose terminal report is still owed is not finished: replacing it
// would drop that report on the floor.
func TestAssignRefusedWhileTerminalReportIsPending(t *testing.T) {
	r := newRig(t, true)
	r.assign()
	r.up.SetFaults(updater.Faults{FailVerification: true})
	for r.up.Snapshot().Stage != "FAILED" {
		if r.up.Snapshot().Stage == "DOWNLOADED" {
			r.link.SetDown(true) // the FAILED report is generated but cannot be delivered
		}
		_, _ = r.up.Step(ctx)
	}
	snap := r.up.Snapshot()
	require.True(t, snap.Pending)

	next := r.asg
	next.AttemptID, next.AssignmentID = "att-2", "asg-2"
	assert.ErrorIs(t, r.up.Assign(next), updater.ErrAttemptInProgress)

	r.link.SetDown(false)
	r.runToEnd()
	assert.Equal(t, ota.StateFailed, r.state(), "the terminal report was delivered")
	require.NoError(t, r.up.Assign(next), "now it has ended and been told")
}
