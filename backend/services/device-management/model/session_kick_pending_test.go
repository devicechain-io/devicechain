// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// togglingKicker is a responder that can be taken down and brought back, and records every kick
// it was asked for, whether it answered or not.
type togglingKicker struct {
	mu    sync.Mutex
	up    bool
	calls []togglingCall
}

type togglingCall struct {
	reason string
	tokens []string
}

func (k *togglingKicker) KickDeviceSessions(_ context.Context, _, reason string, tokens []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, togglingCall{reason, append([]string(nil), tokens...)})
	if !k.up {
		return errors.New("no responder")
	}
	return nil
}

func (k *togglingKicker) set(up bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.up = up
}

func (k *togglingKicker) answered(token string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.up && len(k.calls) > 0 && k.calls[len(k.calls)-1].tokens[0] == token
}

func (f *provFixture) pendingKicks() []PendingSessionKick {
	f.t.Helper()
	var rows []PendingSessionKick
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Order("device_token, origin").Find(&rows).Error)
	return rows
}

// A revoke and a reset of the SAME device are two debts. The second must not replace the first's
// owner: after revoke A fails and a reset of its device fails, repeating revoke A while the
// responder is still down must report the failure, not success.
func TestARepeatedRevokeStillOwesItsKickAfterAResetOfTheSameDevice(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	kicker := &togglingKicker{}
	f.api.SessionKicker = kicker
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	dev := core.DeriveDeviceToken("SN-1", "pv-")
	profileID := f.profileID("pp-1")

	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	_, err = f.api.ResetDeviceProvisioning(f.ctx, dev)
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	rows := f.pendingKicks()
	require.Len(t, rows, 2, "the revoke and the reset each owe their own kick")
	assert.Equal(t, []uint{kickOriginReset, profileID}, []uint{rows[0].Origin, rows[1].Origin})

	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded, "the revoke was told success while the session was live")

	kicker.set(true)
	n, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.True(t, kicker.answered(dev))
	// The revoke settled its own debt; the reset still owes its own, and its repeat clears it.
	rows = f.pendingKicks()
	require.Len(t, rows, 1)
	assert.Equal(t, kickOriginReset, rows[0].Origin)
	_, err = f.api.ResetDeviceProvisioning(f.ctx, dev)
	require.NoError(t, err)
	assert.Empty(t, f.pendingKicks())
}

// Two profiles' revokes over one device: a repeat of the first, with the responder still down,
// must not report success because the second revoke wrote its own row.
func TestARepeatedRevokeStillOwesItsKickAfterAnotherProfilesRevoke(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.profile("pp-2", ProvisionAllowNew)
	kicker := &togglingKicker{}
	f.api.SessionKicker = kicker
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	dev := f.deviceByExternal("SN-1")
	// The device also holds a credential pp-2 issued.
	pp2 := f.profileID("pp-2")
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceCredential{
		DeviceId: dev.ID, TokenReference: rdb.TokenReference{Token: "cred-2"}, CredentialType: "ACCESS_TOKEN", CredentialId: "second-credential", Enabled: true,
		ProvisioningProfileId: &pp2,
	}).Error)

	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-2")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Len(t, f.pendingKicks(), 2)

	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded, "the first revoke was told success while the session was live")
	kicker.set(true)
	_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
}

// The retry does not wait for an operator. With no one repeating anything, a pass of the
// background drain confirms the kick and clears the row; with the responder down it leaves it.
func TestTheBackgroundDrainRetriesWithoutAnOperator(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	kicker := &togglingKicker{}
	f.api.SessionKicker = kicker
	for i, ext := range []string{"SN-1", "SN-2"} {
		_, err := f.register("pp-1", ext, material(i+1))
		require.NoError(t, err)
	}
	_, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	_, err = f.api.ResetDeviceProvisioning(f.ctx, core.DeriveDeviceToken("SN-1", "pv-"))
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Len(t, f.pendingKicks(), 3)

	var pending int
	var oldest time.Duration
	f.api.SessionKickObserver = func(p int, o time.Duration) { pending, oldest = p, o }
	f.api.observeSessionKicks(f.ctx)
	assert.Equal(t, 3, pending)
	assert.Greater(t, oldest, time.Duration(0))

	res, err := f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.Error(t, err, "the responder is down, so the pass did not complete")
	assert.Equal(t, 1, res.Failed)
	require.Len(t, f.pendingKicks(), 3, "an unconfirmed kick must stay owed")

	kicker.set(true)
	kicker.mu.Lock()
	kicker.calls = nil
	kicker.mu.Unlock()
	res, err = f.api.DrainSessionKicks(f.ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Tenants)
	assert.Empty(t, f.pendingKicks())
	// SN-1 owes under two origins and is kicked once, under the strongest reason.
	byDevice := map[string][]string{}
	for _, c := range kicker.calls {
		for _, tk := range c.tokens {
			byDevice[tk] = append(byDevice[tk], c.reason)
		}
	}
	assert.Equal(t, []string{messaging.SessionKickCredentialDeleted}, byDevice[core.DeriveDeviceToken("SN-1", "pv-")])
	assert.Equal(t, []string{messaging.SessionKickCredentialChanged}, byDevice[core.DeriveDeviceToken("SN-2", "pv-")])

	f.api.observeSessionKicks(f.ctx)
	assert.Zero(t, pending)
	assert.Zero(t, oldest)
}

// A row re-queued while its kick is in flight survives the delete that follows the confirmation:
// the confirmation was for the earlier withdrawal.
func TestAKickConfirmedForAnEarlierWriteDoesNotClearALaterOne(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	dev := core.DeriveDeviceToken("SN-1", "pv-")
	f.api.SessionKicker = &togglingKicker{}
	_, err = f.api.ResetDeviceProvisioning(f.ctx, dev)
	require.ErrorIs(t, err, ErrSessionsNotEnded)

	f.api.SessionKicker = kickRecorder(func(context.Context, string, string, []string) error {
		// A newer withdrawal for the same device commits while this kick is on the wire.
		require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
			return f.api.queueSessionKicksOnTx(tx, "acme", kickOriginReset, []string{dev})
		}))
		return nil
	})
	require.NoError(t, f.api.drainSessionKicks(f.ctx, "acme", 0, func(db *gorm.DB) *gorm.DB { return db }))
	require.Len(t, f.pendingKicks(), 1, "the newer withdrawal was swallowed by the older confirmation")
}

// A re-queue records a newer withdrawal: its time and generation move.
func TestRequeueingAKickRefreshesItsTimeAndGeneration(t *testing.T) {
	f := newProvFixture(t)
	f.api.SessionKicker = &togglingKicker{}
	queue := func() {
		require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
			return f.api.queueSessionKicksOnTx(tx, "acme", 7, []string{"dev-1"})
		}))
	}
	queue()
	first := f.pendingKicks()[0]
	time.Sleep(5 * time.Millisecond)
	queue()
	rows := f.pendingKicks()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].RequestedAt.After(first.RequestedAt), "the request time did not move")
	assert.NotEqual(t, first.Generation, rows[0].Generation)
}
