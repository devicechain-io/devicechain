// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feed applies a sequence of reports, each of which must be APPLIED, and returns the result.
func feed(t *testing.T, a Attempt, rs ...Report) Attempt {
	t.Helper()
	for _, r := range rs {
		a = mustApply(t, a, r, VerdictApplied)
	}
	return a
}

func withBoot(r Report, boot string) Report { r.BootID = boot; return r }

func queued() Attempt {
	a := attemptIn(StateQueued, 0)
	a.LastReportAt = t0
	return a
}

// An unrelated power cycle before installation must not count as the reboot the update needed.
func TestRebootConfirmationAnchorsOnTheInstallBoot(t *testing.T) {
	a := feed(t, queued(),
		withBoot(progressReport(StageReceived, 1), "boot-a"),
		withBoot(progressReport(StageDownloading, 2), "boot-b"), // power cycle while downloading
		withBoot(progressReport(StageInstalling, 3), "boot-b"),
	)
	// RUNNING on the same boot the install began on: the device never restarted into the new image.
	mustApply(t, a, runningReport(4, "boot-b"), VerdictUnconfirmed)
	// A genuine restart after installing confirms.
	out := mustApply(t, a, runningReport(4, "boot-c"), VerdictApplied)
	assert.Equal(t, StateUpdated, out.State)

	// The same holds when the attempt went UNKNOWN and is settled by inventory.
	u, changed, err := Tick(a, testPolicy, t0.Add(testPolicy.Install+time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, StateUnknown, u.State)
	got, v := Reconcile(u, inventoryReport("2.0.0", "boot-b"), t0)
	assert.Equal(t, VerdictUnconfirmed, v)
	assert.Equal(t, u, got)
	got, v = Reconcile(u, inventoryReport("2.0.0", "boot-c"), t0)
	assert.Equal(t, VerdictApplied, v)
	assert.Equal(t, StateUpdated, got.State)
	// Rebooted onto another version is judged against the install boot too.
	got, v = Reconcile(u, inventoryReport("1.0.0", "boot-b"), t0)
	assert.Equal(t, VerdictUnconfirmed, v, "still the install boot: nothing shown")
	got, v = Reconcile(u, inventoryReport("1.0.0", "boot-c"), t0)
	assert.Equal(t, VerdictApplied, v)
	assert.Equal(t, StateFailed, got.State)
}

// Pins which boot is the anchor, not just that one exists.
func TestInstallBootIsTheAnchorNotTheLatestBoot(t *testing.T) {
	// INSTALLING on boot-a, then REBOOTING reported from boot-b (the device had already restarted).
	// The anchor is the install boot (a), so RUNNING on boot-b is a genuine reboot. An anchor that
	// followed the latest report (b) would call this unconfirmed.
	a := feed(t, queued(),
		withBoot(progressReport(StageReceived, 1), "boot-a"),
		withBoot(progressReport(StageInstalling, 2), "boot-a"),
		withBoot(progressReport(StageRebooting, 3), "boot-b"),
	)
	assert.Equal(t, "boot-a", a.InstallBootID)
	out := mustApply(t, a, runningReport(4, "boot-b"), VerdictApplied)
	assert.Equal(t, StateUpdated, out.State)

	// Later install-stage reports do not move the anchor.
	a2 := feed(t, a, withBoot(progressReport(StageRebooting, 4), "boot-c"))
	assert.Equal(t, "boot-a", a2.InstallBootID)
	mustApply(t, a2, runningReport(5, "boot-a"), VerdictUnconfirmed)
}

// With no install-stage report at all, the anchor falls back to the last applied report's boot.
func TestAnchorFallsBackToTheLastAppliedBoot(t *testing.T) {
	a := feed(t, queued(),
		withBoot(progressReport(StageReceived, 1), "boot-a"),
		withBoot(progressReport(StageDownloaded, 2), "boot-b"),
	)
	assert.Equal(t, "", a.InstallBootID)
	mustApply(t, a, runningReport(3, "boot-b"), VerdictUnconfirmed)
	out := mustApply(t, a, runningReport(3, "boot-c"), VerdictApplied)
	assert.Equal(t, StateUpdated, out.State)
}

func TestInvalidStateAndStageAreLoudNotPanics(t *testing.T) {
	noPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("%s panicked: %v", name, p)
			}
		}()
		f()
	}

	bad := attemptIn(StateUpdating, 5)
	bad.State = State("updating") // wrong case: not a declared state
	noPanic("Apply on an undefined state", func() {
		out, v := Apply(bad, progressReport(StageInstalling, 6), t0)
		assert.Equal(t, VerdictInvalidAttempt, v)
		assert.Equal(t, bad, out)
	})
	noPanic("Tick on an undefined state", func() {
		out, changed, err := Tick(bad, testPolicy, t0.Add(100*time.Hour))
		assert.ErrorIs(t, err, ErrInvalidAttempt)
		assert.False(t, changed)
		assert.Equal(t, bad, out)
	})
	noPanic("Reconcile on an undefined state", func() {
		_, v := Reconcile(bad, inventoryReport("2.0.0", "boot-b"), t0)
		assert.Equal(t, VerdictInvalidAttempt, v)
	})
	noPanic("Apply with a bogus stage on an unvalidated report", func() {
		good := attemptIn(StateUpdating, 5)
		r := progressReport(ReportStage("BOGUS"), 6)
		_, v := Apply(good, r, t0)
		assert.Equal(t, VerdictWrongKind, v)
		r.Stage = ReportStage("installing")
		_, v = Apply(good, r, t0)
		assert.Equal(t, VerdictWrongKind, v)
	})
}

func TestTickRejectsAnInvalidPolicy(t *testing.T) {
	a := attemptIn(StateUpdating, 5)
	for _, p := range []Policy{{}, {Acknowledge: 1, Download: 1, Verify: 1, Install: 1}, {Acknowledge: -1, Download: 1, Verify: 1, Install: 1, Confirm: 1}} {
		out, changed, err := Tick(a, p, t0.Add(time.Hour))
		assert.Error(t, err)
		assert.False(t, changed)
		assert.Equal(t, a, out)
	}
}

// Every progress cell of the table is derivable from the ranks: a stage at or above the current
// state's rank is progress, below it is a regression.
func TestTableAgreesWithRanks(t *testing.T) {
	checked := 0
	for _, s := range allStates {
		sr, ranked := rank(s)
		if !ranked {
			continue
		}
		for _, st := range allStages {
			target, isProgress := stateForStage(st)
			if !isProgress {
				continue
			}
			tr, ok := rank(target)
			require.True(t, ok)
			want := ruleRegress
			if tr >= sr {
				want = ruleProgress
			}
			assert.Equal(t, want, transitions[s][st], "(%s, %s)", s, st)
			checked++
		}
	}
	assert.Equal(t, 7*6, checked)
}

func TestRebootingRejectsInstalling(t *testing.T) {
	mustApply(t, attemptIn(StateRebooting, 6), progressReport(StageInstalling, 7), VerdictRegression)
	mustApply(t, attemptIn(StateUpdating, 5), progressReport(StageVerified, 6), VerdictRegression)
}

func TestFailureStageNamesTheStageItFailedIn(t *testing.T) {
	cases := map[State]ReportStage{
		StateQueued: "", StateInitiated: StageReceived, StateDownloading: StageDownloading,
		StateDownloaded: StageDownloaded, StateVerified: StageVerified, StateUpdating: StageInstalling,
		StateRebooting: StageRebooting, StateUnknown: "",
	}
	for s, want := range cases {
		out := mustApply(t, attemptIn(s, 5), failedReport(6), VerdictApplied)
		require.NotNil(t, out.Failure, string(s))
		assert.Equal(t, want, out.Failure.Stage, "device failure from %s", s)
		assert.False(t, out.Failure.Platform)
		assert.Equal(t, "FLASH_ERROR", out.Failure.Code)
		assert.Equal(t, "bad block", out.Failure.Detail)
	}
	for _, s := range []State{StateQueued, StateInitiated, StateDownloading, StateDownloaded, StateVerified} {
		out, changed, err := Tick(attemptIn(s, 5), testPolicy, t0.Add(100*time.Hour))
		require.NoError(t, err)
		require.True(t, changed)
		assert.Equal(t, stageForState(s), out.Failure.Stage, "deadline from %s", s)
	}
	u := attemptIn(StateUnknown, 6)
	out, _ := Reconcile(u, inventoryReport("1.0.0", "boot-b"), t0)
	require.NotNil(t, out.Failure)
	assert.Equal(t, ReportStage(""), out.Failure.Stage)
}

func TestProgressIsClearedWhenTheStageChanges(t *testing.T) {
	a := attemptIn(StateInitiated, 1)
	r := progressReport(StageDownloading, 2)
	r.Progress = &Progress{Bytes: 10, Total: 100}
	a = mustApply(t, a, r, VerdictApplied)
	require.NotNil(t, a.Progress)
	assert.Equal(t, int64(10), a.Progress.Bytes)

	a = mustApply(t, a, progressReport(StageDownloaded, 3), VerdictApplied)
	assert.Nil(t, a.Progress, "stale download progress must not outlive the stage")
}

func TestSameSeqWithDifferentContentIsAConflict(t *testing.T) {
	a := feed(t, attemptIn(StateVerified, 5), progressReport(StageInstalling, 6))
	before := a.clone()
	// A retransmission is a duplicate; a different report claiming the same seq is a conflict.
	mustApply(t, a, progressReport(StageInstalling, 6), VerdictDuplicate)
	out := mustApply(t, a, runningReport(6, "boot-b"), VerdictConflict)
	assert.Equal(t, before, out)
	assert.Equal(t, StateUpdating, out.State)
}
