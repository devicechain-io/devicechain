// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 A STEP THAT LOOKED UP NOTHING IS A FAILED STEP. When every candidate's device lookup fails
// (device-management unreachable, or not configured for update-management) the step dispatches
// nothing and, read as a success, the rollout keeps reading "running" while the failure counter the
// UpdateRolloutStepsFailing alert watches stays flat.
func TestAStepWhoseEveryDeviceLookupFailedIsAFailedStep(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.create(t, "r1", 2, nil)
	e.devices.err = errors.New("device-management is down")
	// Start steps inline; the failure is only logged there, and the observer sees it.
	var observed []error
	e.api.OnRolloutStep = func(_ RolloutStepResult, err error) { observed = append(observed, err) }
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	require.ErrorContains(t, observed[0], "device-management", "the inline step's failure reached the observer")

	_, err = e.api.StepRollout(tenantCtx("acme"), e.rollout(t, "r1").Rollout.ID)
	require.ErrorContains(t, err, "could be read from device-management")
	require.ErrorIs(t, err, e.devices.err)

	// Counterweight: with device-management back, the same rollout steps cleanly and dispatches.
	e.devices.err = nil
	res := e.step(t, "r1")
	require.Equal(t, 2, res.Dispatched)
}

// A failed query for the RUNNING rollouts stops every step before one is attempted, so it has its
// own hook: otherwise the outage that halts every rollout would not move the counter.
func TestAFailedRunningRolloutListIsObserved(t *testing.T) {
	e := newRolloutEnv(t, 1)
	var failures []error
	e.api.OnRolloutListFailed = func(err error) { failures = append(failures, err) }

	_, err := e.api.AdvanceRollouts(context.Background())
	require.NoError(t, err)
	require.Empty(t, failures, "a healthy list is not a failure")

	require.NoError(t, e.api.RDB.DB(context.Background()).Migrator().DropTable(&UpdateRollout{}))
	_, err = e.api.AdvanceRollouts(context.Background())
	require.Error(t, err)
	require.Len(t, failures, 1)
	require.Equal(t, err, failures[0])
}

// With downloads switched off, a device asking for a STALE or unknown assignment still gets the
// stale answer, which the service turns into a cancel; only a current assignment is refused as
// not_configured.
func TestAStaleAssignmentIsStillAnsweredWithDownloadsOff(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.api.DownloadBaseURL = ""

	_, err = e.api.GrantDownload(ctx, "dev-1", "no-such-assignment")
	var refusal *GrantRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, GrantUnknownAssignment, refusal.Reason)
	require.True(t, refusal.Stale, "an unknown assignment must still be cancelled on the device")

	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, GrantNotConfigured, refusal.Reason)
	require.False(t, refusal.Stale, "a current assignment is not cancelled, only refused")
}
