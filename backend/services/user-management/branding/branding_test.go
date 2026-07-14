// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package branding

import (
	"encoding/base64"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestMerge(t *testing.T) {
	low := Branding{Title: ptr("DeviceChain"), LogoMaxHeight: ptr(28)}
	high := Branding{Title: ptr("Acme"), Primary: ptr("#112233")}
	got := Merge(high, low)
	if got.Title == nil || *got.Title != "Acme" {
		t.Errorf("title: want Acme, got %v", got.Title)
	}
	if got.Primary == nil || *got.Primary != "#112233" {
		t.Errorf("primary: want #112233, got %v", got.Primary)
	}
	// low tier carries through where high is nil.
	if got.LogoMaxHeight == nil || *got.LogoMaxHeight != 28 {
		t.Errorf("logoMaxHeight: want 28 from low tier, got %v", got.LogoMaxHeight)
	}
	// A field nil at both tiers stays nil (console falls back to built-in).
	if got.Background != nil {
		t.Errorf("background: want nil, got %v", got.Background)
	}
}

func TestValidateEmptyOK(t *testing.T) {
	if err := Validate(Branding{}); err != nil {
		t.Errorf("empty override must be valid: %v", err)
	}
}

func TestValidateHex(t *testing.T) {
	if err := Validate(Branding{Primary: ptr("#1f9fb7")}); err != nil {
		t.Errorf("valid hex rejected: %v", err)
	}
	for _, bad := range []string{"1f9fb7", "#1f9fb", "#gggggg", "#1f9fb7ff", "blue"} {
		if err := Validate(Branding{Accent: ptr(bad)}); err == nil {
			t.Errorf("bad hex %q accepted", bad)
		}
	}
}

func TestValidateTitle(t *testing.T) {
	if err := Validate(Branding{Title: ptr(strings.Repeat("x", MaxTitleLen))}); err != nil {
		t.Errorf("max-length title rejected: %v", err)
	}
	if err := Validate(Branding{Title: ptr(strings.Repeat("x", MaxTitleLen+1))}); err == nil {
		t.Error("over-length title accepted")
	}
	if err := Validate(Branding{Title: ptr("bad\x07bell")}); err == nil {
		t.Error("control-char title accepted")
	}
}

func TestValidateLogoHTTPS(t *testing.T) {
	if err := Validate(Branding{Logo: ptr("https://cdn.example.com/logo.svg")}); err != nil {
		t.Errorf("https svg url rejected: %v", err)
	}
	for _, bad := range []string{"http://example.com/l.png", "ftp://x/y", "https://", "example.com/l.png"} {
		if err := Validate(Branding{Logo: ptr(bad)}); err == nil {
			t.Errorf("bad logo url %q accepted", bad)
		}
	}
}

func dataURI(mime string, n int) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(make([]byte, n))
}

func TestValidateLogoDataURI(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/webp"} {
		if err := Validate(Branding{Logo: ptr(dataURI(mime, 1024))}); err != nil {
			t.Errorf("valid %s data uri rejected: %v", mime, err)
		}
	}
	// SVG is excluded from the data: path (XSS carrier).
	if err := Validate(Branding{Logo: ptr(dataURI("image/svg+xml", 100))}); err == nil {
		t.Error("inline svg data: uri accepted (must be rejected)")
	}
	// Decoded-size ceiling: enforce on decoded bytes, not string length.
	if err := Validate(Branding{Logo: ptr(dataURI("image/png", MaxLogoDecodedBytes))}); err != nil {
		t.Errorf("at-ceiling logo rejected: %v", err)
	}
	if err := Validate(Branding{Logo: ptr(dataURI("image/png", MaxLogoDecodedBytes+1))}); err == nil {
		t.Error("over-ceiling logo accepted")
	}
	// Non-base64 data: URI rejected.
	if err := Validate(Branding{Logo: ptr("data:image/png,rawbytes")}); err == nil {
		t.Error("non-base64 data: uri accepted")
	}
}

func TestValidateLogoHeight(t *testing.T) {
	if err := Validate(Branding{LogoMaxHeight: ptr(28)}); err != nil {
		t.Errorf("valid height rejected: %v", err)
	}
	for _, bad := range []int{0, MinLogoHeight - 1, MaxLogoHeight + 1, -5} {
		if err := Validate(Branding{LogoMaxHeight: ptr(bad)}); err == nil {
			t.Errorf("out-of-range height %d accepted", bad)
		}
	}
}
