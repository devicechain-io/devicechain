// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

// An operator cannot assign an update to a device that is disabled (quarantined), and the
// refusal is typed so a client can tell it from a missing or incompatible device.
func TestAssignRefusesADisabledDevice(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	e.devices.disabled = map[string]bool{"acme/dev-1": true}

	_, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	requireRefusal(t, err, CodeDeviceDisabled)
	require.Empty(t, e.desired.sent, "a refused assignment told the device nothing")

	// Enabling restores it, with nothing left over from the refusal.
	e.devices.disabled = nil
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-1")
	require.NoError(t, err)
	require.Equal(t, string(ota.StateQueued), v.Attempt.State)
}

// 🔴 A DEVICE DISABLED MID-SESSION IS CUT OFF PER MESSAGE. Its connection stays open, so every
// device-facing operation asks device-management again: a report changes nothing, a sync is not
// answered, no download grant is minted and a capability minted earlier is not honoured. Enabling
// the device restores all of it, because nothing was recorded against it.
func TestADeviceDisabledMidUpdateIsCutOffAndRestoredByEnabling(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	g, err := e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.NoError(t, err)
	require.NotNil(t, g)
	c := &Capability{Tenant: "acme", DeviceToken: "dev-1", AssignmentID: v.Assignment.Token,
		Digest: v.Attempt.TargetDigest, ArtifactTok: "fw-1", Expires: e.clock.Add(1e9)}
	_, err = e.api.ResolveDownload(ctx, c)
	require.NoError(t, err)

	e.devices.disabled = map[string]bool{"acme/dev-1": true}
	sentBefore := len(e.desired.sent)

	_, err = e.api.ApplyReport(ctx, "dev-1", progress(v, 1, ota.StageReceived, "boot-1"))
	require.ErrorIs(t, err, ErrDeviceDisabled, "update.progress")
	inv := ota.Report{V: 1, Kind: ota.KindInventory, Component: v.Assignment.Component}
	_, err = e.api.ApplyReport(ctx, "dev-1", inv)
	require.ErrorIs(t, err, ErrDeviceDisabled, "update.inventory")
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.ErrorIs(t, err, ErrDeviceDisabled, "download.request")
	_, err = e.api.CurrentUpdates(ctx, "dev-1")
	require.ErrorIs(t, err, ErrDeviceDisabled, "sync")
	_, err = e.api.ResolveDownload(ctx, c)
	require.ErrorIs(t, err, ErrDownloadDisabled, "a capability minted before the device was disabled")

	require.Equal(t, ota.StateQueued, e.state(t, "acme", v), "a refused report moved the attempt")
	require.Len(t, e.desired.sent, sentBefore, "the device was told something")

	e.devices.disabled = nil
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageReceived, "boot-1"), ota.VerdictApplied)
	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, views, 1)
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.NoError(t, err)
	_, err = e.api.ResolveDownload(ctx, c)
	require.NoError(t, err)
}

var errUnreachable = errors.New("device-management is down")

// A lookup that cannot be answered is not "enabled": the operation fails (and is retried) rather
// than letting a possibly-quarantined device through.
func TestAFailedDeviceLookupFailsTheDeviceFacingOperationClosed(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)

	e.devices.err = errUnreachable
	_, err = e.api.ApplyReport(ctx, "dev-1", progress(v, 1, ota.StageReceived, "boot-1"))
	require.ErrorIs(t, err, errUnreachable)
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.ErrorIs(t, err, errUnreachable)
	_, err = e.api.CurrentUpdates(ctx, "dev-1")
	require.ErrorIs(t, err, errUnreachable)
	require.Equal(t, ota.StateQueued, e.state(t, "acme", v))
}

// A sync from a device with nothing assigned has nothing to refuse and nothing to answer, so it
// costs no lookup: the question is asked only when an answer would be sent.
func TestASyncWithNothingToAnswerAsksDeviceManagementNothing(t *testing.T) {
	e := newUpdateEnv(t)
	views, err := e.api.CurrentUpdates(tenantCtx("acme"), "dev-2")
	require.NoError(t, err)
	require.Empty(t, views)
	require.Zero(t, e.devices.lookups)
}
