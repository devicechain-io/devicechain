// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 AN UPDATE MUST NOT REWRITE CREDENTIAL MATERIAL THE CREATE STORED VERBATIM.
//
// This is the defect the whole partial-update arc exists to remove, in the one place
// it did the most damage — and it was reintroduced by the fix, from the other side.
// The required fold TRIMMED what it accepted, while no create path trims, so:
//
//	create with " s3cret "                 -> stored " s3cret "
//	restate the same secret on update      -> stored "s3cret"
//	the fleet presenting its created secret -> "provision secret did not match"
//
// A client that reads an entity and sends its fields back is not exotic: it is what
// every convergence loop does, including the simulator's own ensure* paths, whose
// comment describes the request as a full restatement. So a padded secret — legal on
// create today — was silently rotated by an edit that reported success, and the whole
// fleet stopped registering.
//
// These drive the real Api rather than the fold, because the fold passing in isolation
// is exactly what was true while this was broken: the asymmetry lives BETWEEN create
// and update, so only a test that uses both can see it.

func TestRestatingAProvisioningSecretDoesNotRotateIt(t *testing.T) {
	// Padded on purpose. CreateProvisioningProfile stores it exactly, so this is a
	// value a real profile can hold.
	const padded = " s3cret "
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{})...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: " fleet-key ", ProvisionSecret: padded,
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
	}); err != nil {
		t.Fatalf("seed provisioning profile: %v", err)
	}

	// The restatement: every field sent back exactly as stored.
	if _, err := api.UpdateProvisioningProfile(ctx, "pp-1", &ProvisioningProfileUpdateRequest{
		ProvisionKey:    dcgraphql.OptionalStringOf(" fleet-key "),
		ProvisionSecret: dcgraphql.OptionalStringOf(padded),
		Strategy:        dcgraphql.OptionalStringOf(string(ProvisionAllowNew)),
		DeviceTypeToken: dcgraphql.OptionalStringOf("dt"),
		Enabled:         dcgraphql.OptionalBoolOf(true),
	}); err != nil {
		t.Fatalf("restating the stored fields was refused: %v", err)
	}

	rows, err := api.ProvisioningProfilesByToken(ctx, []string{"pp-1"})
	p := requireOne(t, "provisioning profile", rows, err)
	if p.ProvisionSecret != padded {
		t.Errorf("provisionSecret = %q, want %q — restating the secret rotated it, and the "+
			"fleet presenting the created one now fails to register", p.ProvisionSecret, padded)
	}
	if p.ProvisionKey != " fleet-key " {
		t.Errorf("provisionKey = %q, want %q — the key a fleet presents to be LOOKED UP was "+
			"rewritten, so nothing resolves to this profile any more", p.ProvisionKey, " fleet-key ")
	}
}

func TestRestatingACredentialIdDoesNotRewriteIt(t *testing.T) {
	const padded = " a-minted-bearer "
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &DeviceCredential{})...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "dcred-1", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: padded, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	if _, err := api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{
		CredentialType: dcgraphql.OptionalStringOf(string(CredentialAccessToken)),
		CredentialId:   dcgraphql.OptionalStringOf(padded),
		Enabled:        dcgraphql.OptionalBoolOf(true),
	}); err != nil {
		t.Fatalf("restating the stored fields was refused: %v", err)
	}

	rows, err := api.DeviceCredentialsByToken(ctx, []string{"dcred-1"})
	c := requireOne(t, "device credential", rows, err)
	if c.CredentialId != padded {
		t.Errorf("credentialId = %q, want %q — the identifier the DEVICE presents at connect "+
			"time was rewritten by an edit that restated it", c.CredentialId, padded)
	}
}

// THE COUNTERWEIGHT. Both tests above are satisfied by an update that writes nothing at
// all, so one of them has to show a real change still landing.
func TestRotatingAProvisioningSecretStillTakes(t *testing.T) {
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{})...)
	ctx := partialUpdateCtx()
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", ProvisionSecret: " s3cret ",
		Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
	}); err != nil {
		t.Fatalf("seed provisioning profile: %v", err)
	}
	if _, err := api.UpdateProvisioningProfile(ctx, "pp-1", &ProvisioningProfileUpdateRequest{
		ProvisionSecret: dcgraphql.OptionalStringOf("rotated"),
	}); err != nil {
		t.Fatalf("a rotation was refused: %v", err)
	}
	rows, err := api.ProvisioningProfilesByToken(ctx, []string{"pp-1"})
	if got := requireOne(t, "provisioning profile", rows, err).ProvisionSecret; got != "rotated" {
		t.Fatalf("provisionSecret = %q, want %q", got, "rotated")
	}
}
