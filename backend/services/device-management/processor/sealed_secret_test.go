// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dcmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// The secret an event carries is checked after it is opened, and only a secret sealed for
// THIS credential in THIS tenant opens. Everything else, including the plaintext an older
// event-sources left in a message that was still in the stream at upgrade, fails to
// authenticate as a wrong password: Unauthenticated, never an absent credential.
func TestOnlyASecretSealedForThisCredentialAuthenticates(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	secret := "a-sufficiently-long-secret"
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &dcmodel.DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(dcmodel.CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	// A second credential with the SAME secret, so a secret sealed for the first and
	// presented as the second fails on the binding alone, not on a missing credential.
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &dcmodel.DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(dcmodel.CredentialMqttBasic),
		CredentialId: "cred-2", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatalf("seed second credential: %v", err)
	}
	mqtt := string(dcmodel.CredentialMqttBasic)
	resolve := func(rez *EventResolver, mutate func(*esmodel.UnresolvedEvent)) (uint, error) {
		e := tempEvent("21")
		presentSealed(t, rig.ctx, e, mqtt, "cred-1", secret)
		mutate(e)
		_, reason, err := rez.ResolveEvent(rig.ctx, e)
		return reason, err
	}
	keyed := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	keyed.SecretTransit = testSecretTransit(t)
	keyless := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)

	if reason, err := resolve(keyed, func(*esmodel.UnresolvedEvent) {}); err != nil {
		t.Fatalf("control: a correctly sealed secret must authenticate: reason %d, %v", reason, err)
	}

	otherCred := "cred-2"
	plaintext := secret
	for name, c := range map[string]struct {
		rez    *EventResolver
		mutate func(*esmodel.UnresolvedEvent)
	}{
		"the plaintext of an older message": {keyed, func(e *esmodel.UnresolvedEvent) { e.SealedCredentialSecret = []byte(plaintext) }},
		"sealed for another credential id":  {keyed, func(e *esmodel.UnresolvedEvent) { e.CredentialId = &otherCred }},
		"no key to open it with":            {keyless, func(*esmodel.UnresolvedEvent) {}},
	} {
		reason, err := resolve(c.rez, c.mutate)
		if err == nil {
			t.Errorf("%s: authenticated", name)
			continue
		}
		if reason != uint(resolvedevent.FailureReason_Unauthenticated) {
			t.Errorf("%s: reason %d, want Unauthenticated", name, reason)
		}
	}
}
