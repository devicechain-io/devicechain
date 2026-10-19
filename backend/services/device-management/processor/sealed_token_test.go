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

// An access token is opened before the credential is looked up, and only a token sealed
// for THIS tenant and credential type authenticates. Everything else, including the clear
// id an older event-sources left in a message that was still in the stream at upgrade,
// fails as Unauthenticated, in optional mode too: never as an absent credential.
func TestOnlyATokenSealedForThisTenantAndTypeAuthenticates(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	const token = "tok-0123456789abcdef"
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &dcmodel.DeviceCredentialCreateRequest{
		Token: "c-tok", DeviceToken: "dev", CredentialType: string(dcmodel.CredentialAccessToken),
		CredentialId: token, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	access, mqtt := string(dcmodel.CredentialAccessToken), string(dcmodel.CredentialMqttBasic)
	mode := func(m string, transit bool) *EventResolver {
		rez := NewEventResolver(1, rig.capi, m, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
		if transit {
			rez.SecretTransit = testSecretTransit(t)
		}
		return rez
	}
	resolve := func(rez *EventResolver, mutate func(*esmodel.UnresolvedEvent)) (uint, error) {
		e := tempEvent("21")
		presentSealedToken(t, rig.ctx, e, access, token)
		mutate(e)
		_, reason, err := rez.ResolveEvent(rig.ctx, e)
		return reason, err
	}

	for _, m := range []string{config.AuthModeRequired, config.AuthModeOptional} {
		if reason, err := resolve(mode(m, true), func(*esmodel.UnresolvedEvent) {}); err != nil {
			t.Fatalf("%s control: a correctly sealed token must authenticate: reason %d, %v", m, reason, err)
		}
		foreign, _ := testSecretTransit(t).SealToken("other-tenant", access, token)
		wrongType, _ := testSecretTransit(t).SealToken("acme", mqtt, token)
		asSecret, _ := testSecretTransit(t).Seal("acme", token, "x-secret-value")
		clear := token
		for name, c := range map[string]struct {
			rez    *EventResolver
			mutate func(*esmodel.UnresolvedEvent)
		}{
			"the clear id of an older message":   {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId, e.CredentialId = nil, &clear }},
			"the token itself in the sealed id":  {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = []byte(clear) }},
			"sealed for another tenant":          {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = foreign }},
			"sealed for another credential type": {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = wrongType }},
			"a sealed password":                  {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = asSecret }},
			"no key to open it with":             {mode(m, false), func(*esmodel.UnresolvedEvent) {}},
		} {
			reason, err := resolve(c.rez, c.mutate)
			if err == nil {
				t.Errorf("%s/%s: authenticated", m, name)
				continue
			}
			if reason != uint(resolvedevent.FailureReason_Unauthenticated) {
				t.Errorf("%s/%s: reason %d, want Unauthenticated", m, name, reason)
			}
		}
	}
}

// A sealed token beside a sealed secret is a shape event-sources never publishes. It is
// refused as Unauthenticated, and resolving it must not dereference the clear id the
// sealed form leaves empty.
func TestASealedTokenBesideASecretIsRefusedNotPanicked(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	const token = "tok-0123456789abcdef"
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &dcmodel.DeviceCredentialCreateRequest{
		Token: "c-tok", DeviceToken: "dev", CredentialType: string(dcmodel.CredentialAccessToken),
		CredentialId: token, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	for _, m := range []string{config.AuthModeRequired, config.AuthModeOptional} {
		rez := NewEventResolver(1, rig.capi, m, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
		rez.SecretTransit = testSecretTransit(t)
		e := tempEvent("21")
		presentSealedToken(t, rig.ctx, e, string(dcmodel.CredentialAccessToken), token)
		sealed, err := testSecretTransit(t).Seal("acme", token, "a-sufficiently-long-secret")
		if err != nil {
			t.Fatal(err)
		}
		e.SealedCredentialSecret = sealed
		_, reason, err := rez.ResolveEvent(rig.ctx, e)
		if err == nil || reason != uint(resolvedevent.FailureReason_Unauthenticated) {
			t.Errorf("%s: reason %d, err %v, want Unauthenticated", m, reason, err)
		}
	}
}

// An event with a sealed id and no credential type presents nothing it can be checked
// against, so it is refused rather than read as an absent credential.
func TestATypelessSealedIdIsRefused(t *testing.T) {
	rez := &EventResolver{SecretTransit: testSecretTransit(t)}
	sealed := []byte("\xd2sealed")
	got, err := rez.presentedCredential(credCtx(), &esmodel.UnresolvedEvent{SealedCredentialId: sealed})
	if err == nil || got != nil {
		t.Fatalf("got %v, %v; want a refusal", got, err)
	}
}
