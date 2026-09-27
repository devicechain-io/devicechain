// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"math"
	"testing"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/iam"
)

// 🔴 A LOGO HEIGHT WIDER THAN A GraphQL Int IS REFUSED ON READ, NOT WRAPPED.
//
// logoMaxHeight is a bigint column behind a Go int, and the wire type is a 32-bit Int. The
// read used to narrow with a bare int32 conversion, so a stored 2147483648 read back as
// -2147483648. The API bounds a height to 16..200, so only an out-of-band write reaches
// this — and the refusal is not free for the console (see LogoMaxHeight's comment): it
// fails the tenant's boot query, which is why a valid height must be written back.
func TestALogoHeightWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	wide := math.MaxInt32 + 1

	// The raw override is what the branding editor reads, so it is read through the
	// resolver it is served by rather than a hand-built branding value.
	override := (&TenantResolver{t: &iam.Tenant{BrandingLogoMaxHeight: &wide}}).BrandingOverride()
	got, err := override.LogoMaxHeight()
	if got != nil {
		t.Errorf("brandingOverride.logoMaxHeight 2147483648 read back as %d, want no value", *got)
	}
	if !errors.Is(err, util.ErrStoredIntOutOfRange) {
		t.Errorf("brandingOverride.logoMaxHeight 2147483648: err = %v, want ErrStoredIntOutOfRange", err)
	}

	// The resolved branding goes through the same resolver type.
	got, err = (&TenantBrandingResolver{b: branding.Branding{LogoMaxHeight: &wide}}).LogoMaxHeight()
	if got != nil || !errors.Is(err, util.ErrStoredIntOutOfRange) {
		t.Errorf("branding.logoMaxHeight 2147483648: got (%v, %v), want a refusal and no value", got, err)
	}

	// Counterweights: an in-range height reads back as itself, and no height stays null.
	h := 200
	if got, err := (&TenantBrandingResolver{b: branding.Branding{LogoMaxHeight: &h}}).LogoMaxHeight(); err != nil || got == nil || *got != 200 {
		t.Errorf("logoMaxHeight 200: got (%v, %v), want 200", got, err)
	}
	if got, err := (&TenantBrandingResolver{}).LogoMaxHeight(); got != nil || err != nil {
		t.Errorf("no logoMaxHeight: got (%v, %v), want (nil, nil)", got, err)
	}
}
