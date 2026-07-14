// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import "testing"

func TestProxyLogoValue(t *testing.T) {
	ptr := func(s string) *string { return &s }

	// An object-store ref becomes the read-proxy path, versioned by the object id
	// (so the URL changes on every upload) — the raw ref is never exposed.
	ref := "blob://filesystem/inst1/acme/branding-logo/abcdef123456.png"
	got := proxyLogoValue(ptr(ref))
	if got == nil || *got != brandingLogoPath+"?v=abcdef123456.png" {
		t.Fatalf("blob ref → %v, want %q", got, brandingLogoPath+"?v=abcdef123456.png")
	}

	// Tier-0 values pass through unchanged.
	for _, v := range []string{
		"https://cdn.example.com/logo.png",
		"data:image/png;base64,AAAA",
	} {
		if got := proxyLogoValue(ptr(v)); got == nil || *got != v {
			t.Fatalf("Tier-0 %q → %v, want passthrough", v, got)
		}
	}

	// A nil column stays nil (inherit).
	if proxyLogoValue(nil) != nil {
		t.Fatal("nil logo must map to nil")
	}
}
