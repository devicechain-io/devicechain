// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-user-management/settings"
)

// TestValidateBrandingDefault guards the mint-point validation of the operator
// branding.default tier (ADR-038): a value that would be rejected on the tenant
// path must not be storable via setSetting either, so it can never be served to
// every non-overriding tenant.
func TestValidateBrandingDefault(t *testing.T) {
	// The shipped code default must always pass its own gate.
	for _, d := range settings.Definitions() {
		if d.Key == settings.KeyBrandingDefault {
			if err := validateBrandingDefault(string(d.Default)); err != nil {
				t.Fatalf("code default branding.default rejected: %v", err)
			}
		}
	}

	valid := []string{
		`{}`,
		`{"title":"Acme","logoMaxHeight":40}`,
		`{"primary":"#1f9fb7","accent":"#223344"}`,
		`{"logo":"https://cdn.example.com/logo.svg"}`,
	}
	for _, v := range valid {
		if err := validateBrandingDefault(v); err != nil {
			t.Errorf("valid branding.default %q rejected: %v", v, err)
		}
	}

	invalid := []string{
		`{"primary":"blue"}`, // non-hex
		`{"logo":"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="}`, // inline SVG XSS carrier
		`{"logo":"http://example.com/l.png"}`,                   // http scheme
		`{"logoMaxHeight":5000}`,                                // out of range
		`{"unknownField":"x"}`,                                  // unknown key (DisallowUnknownFields)
		`not json`,
	}
	for _, v := range invalid {
		if err := validateBrandingDefault(v); err == nil {
			t.Errorf("invalid branding.default %q accepted", v)
		}
	}
}
