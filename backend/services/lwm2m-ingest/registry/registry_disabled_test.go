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
