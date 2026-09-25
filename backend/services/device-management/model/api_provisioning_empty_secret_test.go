// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"
)

// A profile holding an EMPTY secret must never match, not even an empty presented one.
// subtle.ConstantTimeCompare("", "") is 1, so without the guard a profile written with
// no secret admits anyone who knows its key and sends nothing.
func TestEvaluateProvisioningProfile_EmptyStoredSecretNeverMatches(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for _, presented := range []string{"", "anything"} {
		err := evaluateProvisioningProfile(profile(true, "", nil), presented, now)
		if !errors.Is(err, ErrProvisioningSecretMismatch) {
			t.Errorf("stored secret \"\", presented %q: got %v, want ErrProvisioningSecretMismatch",
				presented, err)
		}
	}
}

// Create refuses a blank key or secret and writes no row. The whitespace-only cases are
// what separate a TrimSpace check from an == "" one: "  \t" is as much no-secret as "".
func TestCreateProvisioningProfile_RefusesBlankKeyOrSecret(t *testing.T) {
	cases := []struct {
		name        string
		key, secret string
		want        error
	}{
		{"empty secret", "fleet-key", "", ErrProvisioningSecretEmpty},
		{"blank secret", "fleet-key", "  \t", ErrProvisioningSecretEmpty},
		{"empty key", "", "s3cret", ErrProvisioningKeyEmpty},
		{"blank key", " \n ", "s3cret", ErrProvisioningKeyEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{})...)
			ctx := partialUpdateCtx()
			if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
				t.Fatalf("seed device type: %v", err)
			}
			created, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
				Token: "pp-1", ProvisionKey: tc.key, ProvisionSecret: tc.secret,
				Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got (%v, %v), want %v", created, err, tc.want)
			}
			rows, err := api.ProvisioningProfilesByToken(ctx, []string{"pp-1"})
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("the refused create still wrote %d row(s)", len(rows))
			}
		})
	}

	// The counterweight: a padded, non-blank secret is still accepted and stored verbatim.
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{})...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	created, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", ProvisionSecret: " s3cret ",
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
	})
	if err != nil {
		t.Fatalf("a non-blank secret was refused: %v", err)
	}
	if created.ProvisionSecret != " s3cret " {
		t.Fatalf("stored secret %q, want it verbatim", created.ProvisionSecret)
	}
}
