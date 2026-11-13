// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/stretchr/testify/assert"
)

type fakeAuthn struct {
	device *Device
	err    error
	calls  int
}

func (f *fakeAuthn) AuthenticateDevice(context.Context, *PresentedCredential, time.Time) (*Device, error) {
	f.calls++
	return f.device, f.err
}

func presented() *PresentedCredential {
	secret := "s"
	return &PresentedCredential{CredentialType: string(CredentialMqttBasic), CredentialId: "user-1", Secret: &secret}
}

func TestCheckIngestCredential_Mode(t *testing.T) {
	now := time.Now()
	dev := &Device{}
	dev.Token = "d1"
	for name, tc := range map[string]struct {
		mode      string
		presented *PresentedCredential
		device    string
		want      bool
		asks      int
	}{
		"disabled accepts anything, asks nothing":      {config.AuthModeDisabled, presented(), "d1", true, 0},
		"disabled accepts no credential":               {config.AuthModeDisabled, nil, "d1", true, 0},
		"required refuses no credential":               {config.AuthModeRequired, nil, "d1", false, 0},
		"required refuses an empty credential id":      {config.AuthModeRequired, &PresentedCredential{CredentialType: "MQTT_BASIC"}, "d1", false, 0},
		"optional accepts no credential":               {config.AuthModeOptional, nil, "d1", true, 0},
		"required accepts a good credential":           {config.AuthModeRequired, presented(), "d1", true, 1},
		"a credential for another device is refused":   {config.AuthModeRequired, presented(), "someone-else", false, 1},
		"no device named: the credential decides":      {config.AuthModeRequired, presented(), "", true, 1},
		"optional still authenticates a presented one": {config.AuthModeOptional, presented(), "d1", true, 1},
	} {
		t.Run(name, func(t *testing.T) {
			a := &fakeAuthn{device: dev}
			got, err := CheckIngestCredential(context.Background(), a, tc.mode, tc.device, tc.presented, now)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.asks, a.calls)
		})
	}
}

// Every authentication failure is the same refusal; anything else is an error and says nothing
// about the sender.
func TestCheckIngestCredential_Failures(t *testing.T) {
	for _, refusal := range []error{
		ErrCredentialNotPresented, ErrCredentialTypeInvalid, ErrCredentialNotResolved,
		ErrCredentialExpired, ErrCredentialSecretMismatch, ErrCredentialRevoked,
	} {
		got, err := CheckIngestCredential(context.Background(), &fakeAuthn{err: refusal}, config.AuthModeRequired, "d1", presented(), time.Now())
		assert.NoError(t, err, refusal)
		assert.False(t, got, refusal)
	}
	for _, notARefusal := range []error{errors.New("connection refused"), ErrCredentialMisconfigured, context.DeadlineExceeded} {
		got, err := CheckIngestCredential(context.Background(), &fakeAuthn{err: notARefusal}, config.AuthModeRequired, "d1", presented(), time.Now())
		assert.Error(t, err, notARefusal)
		assert.False(t, got, "an error is never an accept: the caller fails open on the error, not on the bool")
	}
}
