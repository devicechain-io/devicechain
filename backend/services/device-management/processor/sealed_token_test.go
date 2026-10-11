// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dcmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
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
			"the token itself in the sealed id":  {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = &clear }},
			"sealed for another tenant":          {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = &foreign }},
			"sealed for another credential type": {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = &wrongType }},
			"a sealed password":                  {mode(m, true), func(e *esmodel.UnresolvedEvent) { e.SealedCredentialId = &asSecret }},
			"no key to open it with":             {mode(m, false), func(*esmodel.UnresolvedEvent) {}},
		} {
			reason, err := resolve(c.rez, c.mutate)
			if err == nil {
				t.Errorf("%s/%s: authenticated", m, name)
				continue
			}
			if reason != uint(dmproto.FailureReason_Unauthenticated) {
				t.Errorf("%s/%s: reason %d, want Unauthenticated", m, name, reason)
			}
		}
	}
}
