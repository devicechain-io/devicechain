// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/hex"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dcmodel "github.com/devicechain-io/dc-device-management/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
)

// Messages an ingest source published before the seal went binary, byte for byte (origin/main
// 9b680acf6, the same two event-sources/proto/legacy_wire_test.go holds): an ACCESS_TOKEN and
// an MQTT password, each sealed in the TEXT form under testSecretTransit's root key for tenant
// "acme". Such a message can sit in the inbound stream across an upgrade, and must
// authenticate on the far side of it exactly as it would have on the near side.
const (
	legacyTokenInboundHex    = "0a04687474701a1164657669636570756c73652d30303030312a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a380242300a2e0a0c0a0474656d70120432312e35121e323032362d31302d31365430393a33303a31342e3938373635343332315a4a0c4143434553535f544f4b454e6a686463733224653239366130656124556542543175674744684a354c34377866503257767a682f6d454166674837614d413865797a6530696a4a464c65595061436536445071477a69644852412b5467765768744533346d30475a475458677466524f4e2f68535377"
	legacyPasswordInboundHex = "0a046d7174741a0a73656e736f722d3030312a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030321e323032362d31302d31365430393a33303a31352e3132353030303030375a380142370a2d0a0434352e3512052d37332e36221e323032362d31302d31365430393a33303a31342e3938373635343332315a0a060a01311201324a0a4d5154545f4241534943520a73656e736f722d3030315a5864637332246532393661306561244e4d48704c56442b33614247712f4c627937787339615546546a6a776b484444444551396844666d305853492f5a655833613376505747327835597a5076306d78762f58647a65304367"
	legacyToken              = "tok-legacy-0123456789abcdef"
)

// A token sealed in the text form, read off a legacy message, authenticates through the
// resolver in required mode; the same seal with one bit changed does not, and fails as
// Unauthenticated rather than as an absent credential.
func TestATokenSealedInTheTextFormStillAuthenticates(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &dcmodel.DeviceCredentialCreateRequest{
		Token: "c-tok", DeviceToken: "dev", CredentialType: string(dcmodel.CredentialAccessToken),
		CredentialId: legacyToken, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	raw, err := hex.DecodeString(legacyTokenInboundHex)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := esproto.UnmarshalUnresolvedEvent(raw)
	if err != nil {
		t.Fatalf("the legacy message does not decode: %v", err)
	}

	rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.SecretTransit = testSecretTransit(t)
	resolve := func(seal []byte) (uint, error) {
		e := tempEvent("21")
		e.CredentialType, e.CredentialId, e.SealedCredentialId = legacy.CredentialType, nil, seal
		_, reason, err := rez.ResolveEvent(rig.ctx, e)
		return reason, err
	}
	if reason, err := resolve(legacy.SealedCredentialId); err != nil {
		t.Fatalf("a token sealed in the text form did not authenticate: reason %d, %v", reason, err)
	}
	tampered := bytes.Clone(legacy.SealedCredentialId)
	tampered[len(tampered)-1] ^= 0x01
	if reason, err := resolve(tampered); err == nil || reason != uint(resolvedevent.FailureReason_Unauthenticated) {
		t.Fatalf("a tampered text-form seal: reason %d, err %v; want Unauthenticated", reason, err)
	}
}

// A password sealed in the text form, read off a legacy message, is presented opened, for
// the credential id it was sealed for.
func TestAPasswordSealedInTheTextFormIsPresentedOpened(t *testing.T) {
	raw, err := hex.DecodeString(legacyPasswordInboundHex)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := esproto.UnmarshalUnresolvedEvent(raw)
	if err != nil {
		t.Fatalf("the legacy message does not decode: %v", err)
	}
	rez := &EventResolver{SecretTransit: testSecretTransit(t)}
	presented, err := rez.presentedCredential(credCtx(), legacy)
	if err != nil || presented == nil || presented.Secret == nil {
		t.Fatalf("presented %+v, %v; want the credential with its secret", presented, err)
	}
	if presented.CredentialId != "sensor-001" || *presented.Secret != "pw-legacy-value" {
		t.Fatalf("presented %q / %q; want sensor-001 / pw-legacy-value", presented.CredentialId, *presented.Secret)
	}
}
