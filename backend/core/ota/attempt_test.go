// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandAckCannotCompleteAnUpdate(t *testing.T) {
	a := attemptIn(StateUpdating, 5)

	// (a) A command-response-shaped body is not a report at all.
	_, err := DecodeReport([]byte(`{"commandToken":"c1","success":true,"dispatchNonce":"n"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	// (b) INSTALLING with a higher seq is progress, and the attempt is still UPDATING.
	a = mustApply(t, a, progressReport(StageInstalling, 6), VerdictApplied)
	assert.Equal(t, StateUpdating, a.State)
	assert.Equal(t, uint64(6), a.LastSeq)

	// (c) An earlier stage does not move it backwards.
	mustApply(t, a, progressReport(StageDownloaded, 7), VerdictRegression)

	// (d) Only a RUNNING report with evidence (the target, on a new boot) completes it.
	a = mustApply(t, a, runningReport(7, "boot-b"), VerdictApplied)
	assert.Equal(t, StateUpdated, a.State)
	assert.False(t, a.Reconciled)
}

func TestRebootWithoutConfirmationIsUnknownNotUpdated(t *testing.T) {
	a := attemptIn(StateRebooting, 6)

	at := t0.Add(testPolicy.Confirm + time.Second)
	a, changed, err := Tick(a, testPolicy, at)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, StateUnknown, a.State)
	assert.False(t, a.State.Terminal())

	again, changed, err := Tick(a, testPolicy, at.Add(24*time.Hour))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, a, again)

	a = mustApply(t, a, runningReport(7, "boot-b"), VerdictApplied)
	assert.Equal(t, StateUpdated, a.State)
	assert.True(t, a.Reconciled)
}

func TestStaleAttemptReportIsRejected(t *testing.T) {
	a := attemptIn(StateDownloading, 2)

	r := progressReport(StageDownloaded, 3)
	r.AttemptID = "att-OLD"
	mustApply(t, a, r, VerdictStaleAttempt)

	r = progressReport(StageDownloaded, 3)
	r.AssignmentID = "asg-OLD"
	mustApply(t, a, r, VerdictStaleAttempt)

	r = progressReport(StageDownloaded, 3)
	r.Component = "bootloader"
	mustApply(t, a, r, VerdictWrongComponent)

	r = progressReport(StageDownloaded, 3)
	r.Kind = KindInventory
	mustApply(t, a, r, VerdictWrongKind)
}

func TestDigestMismatchIsRejected(t *testing.T) {
	a := attemptIn(StateUpdating, 5)

	r := progressReport(StageRebooting, 6)
	r.ArtifactDigest = digestB
	mustApply(t, a, r, VerdictDigestMismatch)

	r = progressReport(StageRebooting, 6)
	r.ArtifactDigest = ""
	mustApply(t, a, r, VerdictDigestMismatch)

	// The envelope matches but the device claims to run a different image.
	r = runningReport(6, "boot-b")
	r.Running.Digest = digestB
	mustApply(t, a, r, VerdictUnconfirmed)

	// A wrong version is not evidence either.
	r = runningReport(6, "boot-b")
	r.Running.Version = "1.9.9"
	mustApply(t, a, r, VerdictUnconfirmed)

	// A RUNNING report with no running block proves nothing (and must not panic).
	r = runningReport(6, "boot-b")
	r.Running = nil
	mustApply(t, a, r, VerdictUnconfirmed)
}

func TestDuplicateReportIsIdempotent(t *testing.T) {
	a := attemptIn(StateInitiated, 1)
	r := progressReport(StageDownloading, 2)
	r.Progress = &Progress{Bytes: 10, Total: 100}

	once := mustApply(t, a, r, VerdictApplied)
	before := once.clone()
	twice, v := Apply(once, r, t0.Add(time.Hour))
	assert.Equal(t, VerdictDuplicate, v)
	assert.True(t, reflect.DeepEqual(before, twice), "a duplicate must leave the attempt byte-identical")

	// The same seq carrying different content is not a retransmission: it is a CONFLICT.
	mustApply(t, once, progressReport(StageVerified, 2), VerdictConflict)
	r2 := r
	r2.Progress = &Progress{Bytes: 11, Total: 100}
	mustApply(t, once, r2, VerdictConflict)
	r3 := r
	r3.BootID = "boot-z"
	mustApply(t, once, r3, VerdictConflict)
}

func TestRegressionIsRejected(t *testing.T) {
	a := attemptIn(StateVerified, 4)
	mustApply(t, a, progressReport(StageDownloading, 5), VerdictRegression)
	mustApply(t, a, progressReport(StageReceived, 5), VerdictRegression)
	mustApply(t, a, progressReport(StageVerified, 3), VerdictRegression) // seq below LastSeq

	// Same stage, higher seq: progress, state unchanged.
	r := progressReport(StageVerified, 5)
	out := mustApply(t, a, r, VerdictApplied)
	assert.Equal(t, StateVerified, out.State)
	assert.Equal(t, uint64(5), out.LastSeq)
}

func TestForwardJumpOverLostReports(t *testing.T) {
	a := attemptIn(StateQueued, 0)
	out := mustApply(t, a, progressReport(StageVerified, 1), VerdictApplied)
	assert.Equal(t, StateVerified, out.State)
	assert.Equal(t, uint64(1), out.LastSeq)
	assert.Equal(t, "boot-a", out.BootIDAtStart)
	assert.Equal(t, t0.Add(time.Second), out.LastReportAt)
}

func TestRunningOnSameBootIsUnconfirmedWhenRebootRequired(t *testing.T) {
	// The device restarted nothing: same boot as the attempt began on.
	a := attemptIn(StateUpdating, 5)
	mustApply(t, a, runningReport(6, "boot-a"), VerdictUnconfirmed)

	// The attempt's very first report is RUNNING: no prior boot to compare with.
	q := attemptIn(StateQueued, 0)
	mustApply(t, q, runningReport(1, "boot-b"), VerdictUnconfirmed)
	// ...and the rejected report recorded nothing.
	out, _ := Apply(q, runningReport(1, "boot-b"), t0)
	assert.Equal(t, "", out.BootIDAtStart)
}

func TestRunningOnSameBootIsUpdatedWhenNoRebootRequired(t *testing.T) {
	a := attemptIn(StateUpdating, 5)
	a.Target.RequiresReboot = false
	out := mustApply(t, a, runningReport(6, "boot-a"), VerdictApplied)
	assert.Equal(t, StateUpdated, out.State)

	// Even as the very first report.
	q := attemptIn(StateQueued, 0)
	q.Target.RequiresReboot = false
	out = mustApply(t, q, runningReport(1, "boot-a"), VerdictApplied)
	assert.Equal(t, StateUpdated, out.State)
}

func TestDeadlinesTimedOutBeforeInstallUnknownAfter(t *testing.T) {
	cases := []struct {
		state    State
		deadline time.Duration
		want     State
	}{
		{StateQueued, testPolicy.Acknowledge, StateTimedOut},
		{StateInitiated, testPolicy.Acknowledge, StateTimedOut},
		{StateDownloading, testPolicy.Download, StateTimedOut},
		{StateDownloaded, testPolicy.Download, StateTimedOut},
		{StateVerified, testPolicy.Verify, StateTimedOut},
		{StateUpdating, testPolicy.Install, StateUnknown},
		{StateRebooting, testPolicy.Confirm, StateUnknown},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			a := attemptIn(c.state, 3)

			same, changed, err := Tick(a, testPolicy, t0.Add(c.deadline)) // exactly on the deadline: not yet
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, a, same)

			out, changed, err := Tick(a, testPolicy, t0.Add(c.deadline+time.Nanosecond))
			require.NoError(t, err)
			require.True(t, changed)
			assert.Equal(t, c.want, out.State)
			assert.NotEqual(t, StateUpdated, out.State)
			if c.want == StateTimedOut {
				require.NotNil(t, out.Failure)
				assert.Equal(t, "DEADLINE", out.Failure.Code)
				assert.True(t, out.Failure.Platform)
				assert.True(t, out.State.Terminal())
			} else {
				assert.Nil(t, out.Failure)
			}
			assert.Equal(t, a.State, attemptIn(c.state, 3).State, "input untouched")
		})
	}

	for _, s := range []State{StateUpdated, StateFailed, StateTimedOut, StateCancelled, StateUnknown} {
		a := attemptIn(s, 3)
		out, changed, err := Tick(a, testPolicy, t0.Add(1000*time.Hour))
		assert.NoError(t, err)
		assert.False(t, changed, string(s))
		assert.Equal(t, a, out, string(s))
	}

	// A policy with a zero deadline must not time everything out.
	bad := testPolicy
	bad.Install = 0
	assert.Error(t, bad.Validate())
	a := attemptIn(StateUpdating, 3)
	out, changed, err := Tick(a, bad, t0.Add(time.Hour))
	assert.Error(t, err, "an invalid policy must be an error, not a silent no-op")
	assert.False(t, changed)
	assert.Equal(t, a, out)
}

func TestUnknownLeavesOnlyOnEvidence(t *testing.T) {
	u := attemptIn(StateUnknown, 6)

	for _, st := range []ReportStage{StageReceived, StageDownloading, StageDownloaded, StageVerified, StageInstalling, StageRebooting} {
		mustApply(t, u, progressReport(st, 7), VerdictLate)
	}
	mustApply(t, u, progressReport(StageAbandoned, 7), VerdictNotCancellable)

	t.Run("running evidence", func(t *testing.T) {
		out := mustApply(t, u, runningReport(7, "boot-b"), VerdictApplied)
		assert.Equal(t, StateUpdated, out.State)
		assert.True(t, out.Reconciled)
	})
	t.Run("failed report", func(t *testing.T) {
		out := mustApply(t, u, failedReport(7), VerdictApplied)
		assert.Equal(t, StateFailed, out.State)
		assert.True(t, out.Reconciled)
		require.NotNil(t, out.Failure)
		assert.Equal(t, "FLASH_ERROR", out.Failure.Code)
		assert.False(t, out.Failure.Platform)
	})
	t.Run("inventory on the target", func(t *testing.T) {
		out, v := Reconcile(u, inventoryReport("2.0.0", "boot-b"), t0.Add(time.Minute))
		assert.Equal(t, VerdictApplied, v)
		assert.Equal(t, StateUpdated, out.State)
		assert.True(t, out.Reconciled)
		assert.Equal(t, t0.Add(time.Minute), out.LastReportAt)
	})
	t.Run("inventory rebooted onto another version", func(t *testing.T) {
		out, v := Reconcile(u, inventoryReport("1.0.0", "boot-b"), t0)
		assert.Equal(t, VerdictApplied, v)
		assert.Equal(t, StateFailed, out.State)
		assert.True(t, out.Reconciled)
		require.NotNil(t, out.Failure)
		assert.Equal(t, "NOT_RUNNING_TARGET", out.Failure.Code)
		assert.True(t, out.Failure.Platform)
	})
	t.Run("inventory same boot other version", func(t *testing.T) {
		out, v := Reconcile(u, inventoryReport("1.0.0", "boot-a"), t0)
		assert.Equal(t, VerdictUnconfirmed, v)
		assert.Equal(t, u, out)
	})
	t.Run("inventory on the target but same boot when a reboot is required", func(t *testing.T) {
		out, v := Reconcile(u, inventoryReport("2.0.0", "boot-a"), t0)
		assert.Equal(t, VerdictUnconfirmed, v)
		assert.Equal(t, u, out)
	})
	t.Run("inventory with no starting boot cannot show a reboot", func(t *testing.T) {
		nb := u
		nb.BootIDAtStart, nb.InstallBootID, nb.LastBootID = "", "", ""
		out, v := Reconcile(nb, inventoryReport("1.0.0", "boot-b"), t0)
		assert.Equal(t, VerdictUnconfirmed, v)
		assert.Equal(t, nb, out)
	})
	t.Run("reconcile refusals", func(t *testing.T) {
		_, v := Reconcile(u, progressReport(StageRunning, 7), t0)
		assert.Equal(t, VerdictWrongKind, v)
		inv := inventoryReport("2.0.0", "boot-b")
		inv.Component = "bootloader"
		_, v = Reconcile(u, inv, t0)
		assert.Equal(t, VerdictWrongComponent, v)
		_, v = Reconcile(attemptIn(StateUpdated, 7), inventoryReport("2.0.0", "boot-b"), t0)
		assert.Equal(t, VerdictLate, v)
		_, v = Reconcile(attemptIn(StateRebooting, 6), inventoryReport("2.0.0", "boot-b"), t0)
		assert.Equal(t, VerdictUnconfirmed, v)
	})
}

func TestCancelBeyondRecall(t *testing.T) {
	cases := []struct {
		state   State
		wantErr error
		want    State
		request bool
	}{
		{StateQueued, nil, StateCancelled, false},
		{StateInitiated, nil, StateInitiated, true},
		{StateDownloading, nil, StateDownloading, true},
		{StateDownloaded, nil, StateDownloaded, true},
		{StateVerified, nil, StateVerified, true},
		{StateUpdating, ErrBeyondRecall, StateUpdating, false},
		{StateRebooting, ErrBeyondRecall, StateRebooting, false},
		{StateUnknown, ErrBeyondRecall, StateUnknown, false},
		{StateUpdated, ErrTerminal, StateUpdated, false},
		{StateFailed, ErrTerminal, StateFailed, false},
		{StateTimedOut, ErrTerminal, StateTimedOut, false},
		{StateCancelled, ErrTerminal, StateCancelled, false},
	}
	require.Len(t, cases, len(allStates))
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			a := attemptIn(c.state, 2)
			out, err := Cancel(a, t0)
			assert.ErrorIs(t, err, c.wantErr)
			assert.Equal(t, c.want, out.State)
			assert.Equal(t, c.request, out.CancelRequested)
			if c.wantErr != nil {
				assert.Equal(t, a, out)
			}
		})
	}

	// ABANDONED without a cancel request is refused wherever it arrives.
	for _, s := range []State{StateQueued, StateInitiated, StateDownloading, StateDownloaded, StateVerified, StateUpdating, StateRebooting, StateUnknown} {
		mustApply(t, attemptIn(s, 2), progressReport(StageAbandoned, 3), VerdictNotCancellable)
	}
	// With one, it cancels up to VERIFIED and is refused beyond.
	for _, s := range []State{StateQueued, StateInitiated, StateDownloading, StateDownloaded, StateVerified} {
		a := attemptIn(s, 2)
		a.CancelRequested = true
		out := mustApply(t, a, progressReport(StageAbandoned, 3), VerdictApplied)
		assert.Equal(t, StateCancelled, out.State, string(s))
	}
	for _, s := range []State{StateUpdating, StateRebooting, StateUnknown} {
		a := attemptIn(s, 2)
		a.CancelRequested = true
		mustApply(t, a, progressReport(StageAbandoned, 3), VerdictNotCancellable)
	}
}

func TestTerminalIsTerminal(t *testing.T) {
	// An explicit list, so the loop cannot go vacuous if Terminal() is wrong.
	terminals := []State{StateUpdated, StateFailed, StateTimedOut, StateCancelled}
	for _, s := range allStates {
		assert.Equal(t, s == StateUpdated || s == StateFailed || s == StateTimedOut || s == StateCancelled, s.Terminal(), string(s))
	}
	for _, s := range terminals {
		a := attemptIn(s, 5)
		for _, st := range allStages {
			r := progressReport(st, 6)
			switch st {
			case StageRunning:
				r.BootID = "boot-b"
				r.Running = &Running{Version: "2.0.0"}
			case StageFailed:
				r.Result = &Result{Code: "X", Detail: "x"}
			}
			mustApply(t, a, r, VerdictLate)
			r.Seq = 5
			// Same seq as the last applied report: identical content is a DUPLICATE, anything else a CONFLICT.
			same := a
			same.LastStage = st
			same.LastBootID = r.BootID
			mustApply(t, same, r, VerdictDuplicate)
			diff := a
			diff.LastStage = StageReceived
			if st == StageReceived {
				diff.LastStage = StageDownloading
			}
			mustApply(t, diff, r, VerdictConflict)
		}
	}
}

func TestTransitionTableIsTotal(t *testing.T) {
	require.Len(t, allStates, 12)
	require.Len(t, allStages, 9)
	for _, s := range allStates {
		assert.True(t, s.Valid(), "Valid() rejects declared state %s", s)
		row, ok := transitions[s]
		require.True(t, ok, "no transition row for state %s", s)
		for _, st := range allStages {
			rl, ok := row[st]
			assert.True(t, ok && rl != 0, "no rule for (%s, %s)", s, st)
		}
		assert.Len(t, row, len(allStages), "row %s has entries for undeclared stages", s)
	}
	for _, st := range allStages {
		assert.True(t, st.Valid(), "Valid() rejects declared stage %s", st)
	}
	assert.Len(t, transitions, len(allStates), "table has rows for undeclared states")
	assert.False(t, State("BOGUS").Valid())
	assert.False(t, ReportStage("BOGUS").Valid())
	assert.False(t, State("").Valid())

	// Terminal() and the table agree: every terminal row rejects every stage.
	for _, s := range allStates {
		if !s.Terminal() {
			continue
		}
		for _, st := range allStages {
			assert.Equal(t, ruleLate, transitions[s][st], "terminal %s must reject %s", s, st)
		}
	}
	// UNKNOWN is the one non-terminal state that is not a ladder rung.
	assert.False(t, StateUnknown.Terminal())
}

func TestApplyIsPure(t *testing.T) {
	rng := newRand(20261009)
	states := allStates
	stages := allStages
	applied, ticked := 0, 0
	for i := 0; i < 2000; i++ {
		a := attemptIn(states[rng.Intn(len(states))], uint64(rng.Intn(5)))
		if rng.Intn(2) == 0 {
			a.Progress = &Progress{Bytes: 1, Total: 2}
			a.Failure = &Failure{Code: "X"}
		}
		a.CancelRequested = rng.Intn(2) == 0
		a.Target.RequiresReboot = rng.Intn(2) == 0
		r := progressReport(stages[rng.Intn(len(stages))], uint64(rng.Intn(8)))
		r.BootID = []string{"boot-a", "boot-b"}[rng.Intn(2)]
		r.Progress = &Progress{Bytes: 5, Total: 10}
		r.Running = &Running{Version: []string{"2.0.0", "1.0.0"}[rng.Intn(2)]}
		r.Result = &Result{Code: "E", Detail: "d"}

		aBefore, rBefore := a.clone(), r.cloneForTest()
		out, v := Apply(a, r, t0)
		require.True(t, reflect.DeepEqual(a, aBefore), "Apply mutated its attempt: %+v vs %+v", a, aBefore)
		require.True(t, reflect.DeepEqual(r, rBefore), "Apply mutated its report")
		if v == VerdictApplied {
			applied++
			// An applied result must not alias the input's pointers (mutating it later must not
			// leak back). A rejection returns the caller's own value, which aliases by definition.
			if out.Progress != nil {
				require.NotSame(t, a.Progress, out.Progress)
				require.NotSame(t, r.Progress, out.Progress)
			}
			if out.Failure != nil {
				require.NotSame(t, a.Failure, out.Failure)
			}
		}

		if _, changed, _ := Tick(a, testPolicy, t0.Add(time.Duration(rng.Intn(3600))*time.Second)); changed {
			ticked++
		}
		Cancel(a, t0)
		inv := inventoryReport([]string{"2.0.0", "1.0.0"}[rng.Intn(2)], []string{"boot-a", "boot-b"}[rng.Intn(2)])
		Reconcile(a, inv, t0)
		require.True(t, reflect.DeepEqual(a, aBefore), "Tick/Cancel/Reconcile mutated their attempt")
	}
	// The sequence must actually exercise the mutating paths, or purity proves nothing.
	assert.Greater(t, applied, 200, "too few APPLIED verdicts for the purity check to mean anything")
	assert.Greater(t, ticked, 100, "too few deadline transitions for the purity check to mean anything")
}

func TestNewAttemptValidates(t *testing.T) {
	tg := Target{Version: "2.0.0", Digest: digestA}
	a, err := NewAttempt("att-1", "asg-1", "firmware", tg, t0)
	require.NoError(t, err)
	assert.Equal(t, StateQueued, a.State)
	assert.Equal(t, t0, a.LastReportAt)

	_, err = NewAttempt("bad id", "asg-1", "firmware", tg, t0)
	assert.Error(t, err)
	_, err = NewAttempt("att-1", "", "firmware", tg, t0)
	assert.Error(t, err)
	_, err = NewAttempt("att-1", "asg-1", "Firmware", tg, t0)
	assert.Error(t, err)
	_, err = NewAttempt("att-1", "asg-1", "firmware", Target{Version: "2.0.0", Digest: "sha256:ABC"}, t0)
	assert.Error(t, err)
	_, err = NewAttempt("att-1", "asg-1", "firmware", Target{Digest: digestA}, t0)
	assert.Error(t, err)
	_, err = NewAttempt("att-1", "asg-1", "firmware", tg, time.Time{})
	assert.Error(t, err)

	assert.NoError(t, testPolicy.Validate())
	assert.Error(t, Policy{}.Validate())
}
