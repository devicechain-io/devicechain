// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

type deviceLookupFunc func(context.Context, string, string) (DeviceFacts, bool, error)

func (f deviceLookupFunc) DeviceOf(ctx context.Context, tenant, device string) (DeviceFacts, bool, error) {
	return f(ctx, tenant, device)
}

func TestDownloadsRejectRecreatedDeviceWithWarmFactsCache(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.devices.incarnations = map[string]uint64{"acme/dev-1": 17}
	a := e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("firmware"))
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-A")
	require.NoError(t, err)
	_, err = e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.NoError(t, err)
	c := &Capability{DeviceToken: "dev-1", AssignmentID: v.Assignment.Token,
		Digest: v.Attempt.TargetDigest, ArtifactTok: a.Token}
	_, err = e.api.ResolveDownload(ctx, c)
	require.NoError(t, err)
	e.devices.incarnations["acme/dev-1"] = 23
	// No TTL advance or deletion fact on this replica.
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	var refusal *GrantRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, GrantUnknownAssignment, refusal.Reason)
	_, err = e.api.ResolveDownload(ctx, c)
	require.ErrorIs(t, err, ErrDownloadStale)
}

func TestForgetPreventsAnInFlightLookupFromRepopulatingTheCache(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	e.api.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return DeviceFacts{Incarnation: 17}, true, nil
		}
		return DeviceFacts{Incarnation: 23}, true, nil
	})
	done := make(chan error, 1)
	go func() {
		_, _, err := e.api.deviceGate(ctx, "dev-1")
		done <- err
	}()
	<-started
	e.api.disabled.forget("acme/dev-1")
	close(release)
	require.NoError(t, <-done)
	facts, found, err := e.api.deviceGate(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(23), facts.Incarnation)
	require.Equal(t, 2, calls)
	_, _, err = e.api.deviceGate(ctx, "dev-1")
	require.NoError(t, err)
	require.Equal(t, 2, calls, "the post-forget answer is cached")
}

func TestLegacyAssignmentWithoutDeviceCreationTimeIsRefused(t *testing.T) {
	require.False(t, assignmentIsForDevice(UpdateAssignment{}, DeviceFacts{Incarnation: 23}))
}

func TestDirectPublicationRechecksCommittedAssignmentAndDevice(t *testing.T) {
	for _, path := range []string{"assign", "rollout"} {
		for _, change := range []string{"same", "recreated", "superseded", "terminal", "disabled", "missing", "lookup error"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				e := newRolloutEnv(t, 1)
				ctx := tenantCtx("acme")
				e.devices.incarnations = map[string]uint64{"acme/dev-0": 17}
				e.create(t, "r1", 1, nil)
				lookups := 0
				e.api.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
					lookups++
					facts := DeviceFacts{Incarnation: 17, Profile: "gateway"}
					var rows []UpdateAssignment
					require.NoError(t, e.api.RDB.DB(ctx).Find(&rows).Error)
					if len(rows) != 0 {
						switch change {
						case "recreated":
							facts.Incarnation = 23
						case "superseded":
							require.NoError(t, e.api.RDB.DB(ctx).Model(&rows[0]).Update("superseded_at", e.api.now()).Error)
						case "terminal":
							require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("assignment_id = ?", rows[0].ID).Update("state", string(ota.StateCancelled)).Error)
						case "disabled":
							facts.Disabled = true
						case "missing":
							return DeviceFacts{}, false, nil
						case "lookup error":
							return DeviceFacts{}, false, errors.New("device-management unavailable")
						}
					}
					return facts, true, nil
				})
				// Even a warm cache must not bypass the publication lookup.
				_, _, err := e.api.deviceGate(ctx, "dev-0")
				require.NoError(t, err)
				if path == "assign" {
					_, err = e.api.AssignArtifact(ctx, "dev-0", "fw-2")
				} else {
					_, err = e.api.StartRollout(ctx, "r1")
				}
				require.NoError(t, err, "publication never fails the committed write")
				require.Equal(t, int64(1), e.assignmentCount(t, "dev-0"))
				require.GreaterOrEqual(t, lookups, 3)
				if change == "same" {
					require.Len(t, e.desired.kinds(t, "dev-0"), 1)
				} else {
					require.Empty(t, e.desired.kinds(t, "dev-0"))
				}
			})
		}
	}
}
