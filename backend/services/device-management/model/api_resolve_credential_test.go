// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
)

// resolveFixture is a real Api over SQLite holding one device, "dev", with an
// MQTT_BASIC credential "cred-1" storing "s3cret".
func resolveFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &DeviceCredential{})...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	secret := "s3cret"
	if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return api, ctx
}

func basic(id, secret string) *PresentedCredential {
	return &PresentedCredential{CredentialType: string(CredentialMqttBasic), CredentialId: id, Secret: &secret}
}

// 🔴 ResolveDeviceCredential DOES NOT COMPARE. A WRONG presented secret still resolves,
// returning the device and the STORED digest: on the callout path the compare lives only
// in credential.Checker, which is what puts it behind the backoff. AuthenticateDevice,
// the per-event path, still compares, and refuses the same presentation.
func TestResolveDeviceCredential_DoesNotCompare(t *testing.T) {
	api, ctx := resolveFixture(t)
	now := time.Now()

	device, stored, err := api.ResolveDeviceCredential(ctx, basic("cred-1", "wrong"), now)
	if err != nil {
		t.Fatalf("a wrong presented secret must still resolve: %v", err)
	}
	if device == nil || device.Token != "dev" {
		t.Fatalf("resolved device %+v, want dev", device)
	}
	if !verifies(stored, "s3cret") {
		t.Fatalf("stored %q is not the credential's own digest", stored)
	}

	// Counterweight: the per-event path compares, and refuses it.
	if _, err := api.AuthenticateDevice(ctx, basic("cred-1", "wrong"), now); !errors.Is(err, ErrCredentialSecretMismatch) {
		t.Fatalf("AuthenticateDevice with a wrong secret: got %v, want ErrCredentialSecretMismatch", err)
	}
	if d, err := api.AuthenticateDevice(ctx, basic("cred-1", "s3cret"), now); err != nil || d.Token != "dev" {
		t.Fatalf("AuthenticateDevice with the right secret: got (%v, %v)", d, err)
	}
}

// Everything short of the compare still applies: an unknown credential, an expired one,
// and one stored with no secret are refused with their sentinels.
func TestResolveDeviceCredential_RefusesWhatAuthenticateDeviceRefuses(t *testing.T) {
	api, ctx := resolveFixture(t)
	now := time.Now()

	if _, _, err := api.ResolveDeviceCredential(ctx, basic("nobody", "x"), now); !errors.Is(err, ErrCredentialNotResolved) {
		t.Errorf("unknown credential: got %v, want ErrCredentialNotResolved", err)
	}

	past := now.Add(-time.Hour)
	if err := api.RDB.DB(ctx).Model(&DeviceCredential{}).Where("credential_id = ?", "cred-1").
		Update("expires_at", sql.NullTime{Time: past, Valid: true}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.ResolveDeviceCredential(ctx, basic("cred-1", "s3cret"), now); !errors.Is(err, ErrCredentialExpired) {
		t.Errorf("expired credential: got %v, want ErrCredentialExpired", err)
	}

	foreignKey, err := credential.DeriveDeviceSecretKey([]byte("another-instance-root-key-32-byt"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := foreignKey.Digest(partialUpdateTenant, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	// No secret, an empty one, the password itself (a row no digest step reached), and a
	// digest of the right password under ANOTHER root key: none can ever authenticate, and
	// none is the device's wrong answer.
	for _, stored := range []sql.NullString{{}, {String: "", Valid: true}, {String: "s3cret", Valid: true}, {String: foreign, Valid: true}} {
		if err := api.RDB.DB(ctx).Model(&DeviceCredential{}).Where("credential_id = ?", "cred-1").
			Updates(map[string]any{"expires_at": sql.NullTime{}, "secret_digest": stored}).Error; err != nil {
			t.Fatal(err)
		}
		if _, _, err := api.ResolveDeviceCredential(ctx, basic("cred-1", "s3cret"), now); !errors.Is(err, ErrCredentialMisconfigured) {
			t.Errorf("stored secret %+v: got %v, want ErrCredentialMisconfigured", stored, err)
		}
	}
}

// A credential type that carries no secret is refused before any lookup: for it there
// is nothing left for a Checker to compare, so a device returned for one would be a
// grant with no check at all.
func TestResolveDeviceCredential_RefusesATypeWithNoSecret(t *testing.T) {
	api := &Api{} // no database: the refusal must come first
	for _, ctype := range []CredentialType{CredentialAccessToken, CredentialX509Certificate} {
		_, _, err := api.ResolveDeviceCredential(context.Background(),
			&PresentedCredential{CredentialType: string(ctype), CredentialId: "tok"}, time.Now())
		if !errors.Is(err, ErrCredentialTypeInvalid) {
			t.Errorf("%s: got %v, want ErrCredentialTypeInvalid", ctype, err)
		}
	}
}

// An EMPTY stored secret is misconfigured on the per-event path too — never a secret an
// empty presented one could match.
func TestEvaluateCredential_BasicEmptyStoredIsMisconfigured(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateCredential(testSecretKey, credentialRow(CredentialMqttBasic, strptr(""), nil), &PresentedCredential{
		CredentialType: string(CredentialMqttBasic), CredentialId: "cred-1", Secret: strptr(""),
	}, now)
	if !errors.Is(err, ErrCredentialMisconfigured) {
		t.Fatalf("got %v, want ErrCredentialMisconfigured", err)
	}
}
