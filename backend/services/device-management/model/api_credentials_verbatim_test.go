// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 A CREDENTIAL VALUE IS STORED EXACTLY AS SENT.
//
// A device presents its MQTT password byte for byte, and neither the per-event compare
// (evaluateCredential) nor the connect callout trims what it presents. The value used to
// be trimmed when it was SAVED, so a password with leading or trailing whitespace was
// stored as something the device never sends, and could never authenticate — the create
// and every rotation returned success over a credential nothing could use.

// verbatimFixture is resolveFixture's device with an MQTT_BASIC credential "cred-v"
// created from the given value.
func verbatimFixture(t *testing.T, value *string) (*Api, context.Context) {
	t.Helper()
	api, ctx := resolveFixture(t)
	if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-v", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-v", CredentialValue: value, Enabled: true,
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return api, ctx
}

func TestACredentialValueIsStoredExactlyAsSent(t *testing.T) {
	api, ctx := verbatimFixture(t, strPtr(" s3cret "))
	now := time.Now()

	_, stored, err := api.ResolveDeviceCredential(ctx, basic("cred-v", "x"), now)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if stored != " s3cret " {
		t.Fatalf("stored secret %q, want %q — the value was rewritten on save, so the device "+
			"presenting what it was configured with can never match it", stored, " s3cret ")
	}
	if d, err := api.AuthenticateDevice(ctx, basic("cred-v", " s3cret "), now); err != nil || d == nil || d.Token != "dev" {
		t.Fatalf("presenting the configured password %q: got (%v, %v), want device dev", " s3cret ", d, err)
	}
	// Counterweight: verbatim means the TRIMMED spelling is now a different password.
	if _, err := api.AuthenticateDevice(ctx, basic("cred-v", "s3cret"), now); !errors.Is(err, ErrCredentialSecretMismatch) {
		t.Fatalf("presenting the trimmed password: got %v, want ErrCredentialSecretMismatch", err)
	}
}

func TestARotatedCredentialValueIsStoredExactlyAsSent(t *testing.T) {
	api, ctx := verbatimFixture(t, strPtr("initial"))
	if _, err := api.UpdateDeviceCredential(ctx, "c-v", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("rotated\t"),
	}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	_, stored, err := api.ResolveDeviceCredential(ctx, basic("cred-v", "x"), time.Now())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if stored != "rotated\t" {
		t.Fatalf("stored secret %q after rotation, want %q", stored, "rotated\t")
	}
}

// A whitespace-only value is a value: it is stored as sent, and the device presenting it
// authenticates. Only an EMPTY value (or an explicit null on update) stores no secret, and
// a credential with no secret is misconfigured rather than matched by an empty
// presentation.
func TestAWhitespaceCredentialValueIsStoredAndOnlyEmptyStoresNone(t *testing.T) {
	now := time.Now()

	t.Run("whitespace on create", func(t *testing.T) {
		api, ctx := verbatimFixture(t, strPtr("   "))
		_, stored, err := api.ResolveDeviceCredential(ctx, basic("cred-v", "x"), now)
		if err != nil || stored != "   " {
			t.Fatalf("resolve: got (%q, %v), want (%q, nil)", stored, err, "   ")
		}
		if _, err := api.AuthenticateDevice(ctx, basic("cred-v", "   "), now); err != nil {
			t.Fatalf("presenting the stored whitespace password: %v", err)
		}
	})

	t.Run("empty on create", func(t *testing.T) {
		api, ctx := verbatimFixture(t, strPtr(""))
		if _, _, err := api.ResolveDeviceCredential(ctx, basic("cred-v", ""), now); !errors.Is(err, ErrCredentialMisconfigured) {
			t.Fatalf("resolve: got %v, want ErrCredentialMisconfigured", err)
		}
	})

	for name, value := range map[string]dcgraphql.OptionalString{
		"empty on update": dcgraphql.OptionalStringOf(""),
		"null on update":  dcgraphql.ClearedString(),
	} {
		t.Run(name, func(t *testing.T) {
			api, ctx := verbatimFixture(t, strPtr("initial"))
			if _, err := api.UpdateDeviceCredential(ctx, "c-v", &DeviceCredentialUpdateRequest{
				CredentialValue: value,
			}); err != nil {
				t.Fatalf("update: %v", err)
			}
			if _, _, err := api.ResolveDeviceCredential(ctx, basic("cred-v", ""), now); !errors.Is(err, ErrCredentialMisconfigured) {
				t.Fatalf("resolve: got %v, want ErrCredentialMisconfigured", err)
			}
		})
	}

	t.Run("whitespace on update", func(t *testing.T) {
		api, ctx := verbatimFixture(t, strPtr("initial"))
		if _, err := api.UpdateDeviceCredential(ctx, "c-v", &DeviceCredentialUpdateRequest{
			CredentialValue: dcgraphql.OptionalStringOf(" "),
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		_, stored, err := api.ResolveDeviceCredential(ctx, basic("cred-v", "x"), now)
		if err != nil || stored != " " {
			t.Fatalf("resolve: got (%q, %v), want (%q, nil)", stored, err, " ")
		}
	})
}
