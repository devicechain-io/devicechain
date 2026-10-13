// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/stretchr/testify/assert"
)

// 🔴 A DISABLED DEVICE CANNOT REGISTER. LwM2M has no broker auth-callout in front of it, so the
// registration is the one door: refused (→ 4.03) BEFORE anything is emitted or installed — no
// CONNECTED, no registration, no observation, and so no device-information harvest. Enabling the
// device lets the very next registration through with no residue.
func TestRegisterRefusedForADisabledDevice(t *testing.T) {
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveFound, disabled: true}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	r := newTestRegistry(res, em, clk, nil)

	result, regId, epoch, establish := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterDeviceDisabled, result)
	assert.Empty(t, regId)
	assert.Zero(t, epoch)
	assert.False(t, establish, "a refused registration must not establish observations")
	assert.Empty(t, em.all(), "a refused registration must emit nothing")
	_, live := r.DeviceToken("dev-1")
	assert.False(t, live, "a refused registration must install nothing")

	res.disabled = false
	result, regId, _, _ = r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterOK, result)
	assert.NotEmpty(t, regId)
	assert.Len(t, em.all(), 1)
}

// The answer cannot be had: the registration is refused as RETRYABLE (5.03), never let through as
// "enabled". The device's own retry re-drives it.
func TestRegisterFailsClosedWhenDisabledStateCannotBeRead(t *testing.T) {
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveFound, disabledErr: errors.New("device-management unreachable")}
	em := &fakeEmitter{}
	r := newTestRegistry(res, em, &testClock{t: time.Unix(1_700_000_000, 0)}, nil)

	result, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterUnavailable, result)
	assert.Empty(t, regId)
	assert.Empty(t, em.all())
}

// 🔴 STORM CONTROL MUST NOT HAND A DISABLED DEVICE ITS OLD LOCATION. A re-register inside the
// replace backoff is normally collapsed onto the live registration without resolving anything;
// for a device disabled since, that would be a 2.01 for a quarantined device. It is checked first,
// and the registration it held is ended (session end fired) rather than left to its timer.
func TestARecentRegistrationDoesNotShieldADisabledDevice(t *testing.T) {
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveFound}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	var ended int
	r := newTestRegistry(res, em, clk, func(o *Options) { o.OnSessionEnd = func(string, uint64) { ended++ } })

	result, _, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterOK, result)

	res.mu.Lock()
	res.disabled = true
	res.mu.Unlock()
	clk.advance(time.Second) // well inside the 5 s replace backoff
	result, regId, _, establish := r.Register(context.Background(), "dev-1", testBinding, 300)
	assert.Equal(t, RegisterDeviceDisabled, result)
	assert.Empty(t, regId, "the old location was handed back")
	assert.False(t, establish)
	assert.Equal(t, 1, ended, "the disabled device's session was not ended")
	_, live := r.DeviceToken("dev-1")
	assert.False(t, live)
}

// EndIfDisabled is the Update-time check: a disabled device's registration is removed and its
// session ended; an enabled one, or one whose state cannot be read, is left alone; and a regId the
// identity does not own is never looked up.
func TestEndIfDisabled(t *testing.T) {
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveFound}
	em := &fakeEmitter{}
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	var ended int
	r := newTestRegistry(res, em, clk, func(o *Options) { o.OnSessionEnd = func(string, uint64) { ended++ } })
	_, regId, _, _ := r.Register(context.Background(), "dev-1", testBinding, 300)
	calls := res.disabledCalls

	assert.False(t, r.EndIfDisabled(context.Background(), "dev-2", regId), "another identity's regId")
	assert.False(t, r.EndIfDisabled(context.Background(), "dev-1", "nope"))
	assert.Equal(t, calls, res.disabledCalls, "a regId that is not the identity's was looked up")

	assert.False(t, r.EndIfDisabled(context.Background(), "dev-1", regId), "enabled")
	res.mu.Lock()
	res.disabledErr = errors.New("device-management unreachable")
	res.disabled = true
	res.mu.Unlock()
	assert.False(t, r.EndIfDisabled(context.Background(), "dev-1", regId), "a failed check must keep the registration")
	assert.Equal(t, UpdateOK, r.Update("dev-1", regId, 300))

	res.mu.Lock()
	res.disabledErr = nil
	res.mu.Unlock()
	assert.True(t, r.EndIfDisabled(context.Background(), "dev-1", regId))
	assert.Equal(t, 1, ended)
	assert.Equal(t, UpdateUnknown, r.Update("dev-1", regId, 300))
	assert.Empty(t, em.disconnects(), "a disabled device's presence is not written: its events are discarded at resolution")
}
