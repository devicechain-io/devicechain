// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package branding is the tenant white-labeling shape and its validation
// (ADR-038 Phase 2). It is a leaf package — it imports neither iam nor settings —
// so both the cascade resolver (which reads a tenant's override columns and the
// system-default setting) and the mutation validators reference one shape.
//
// A Branding is an OVERRIDE: every field is optional (a nil pointer means "inherit
// the next tier down"). The cascade is most-specific-non-null-wins: a tenant's
// override, then the operator's `branding.default` system setting, then the code
// default that ships with that setting (settings/model.go). A field that is nil at
// every tier resolves to nil, and the console then falls back to its built-in
// look for that field (a null primary keeps the shipped palette, a null logo keeps
// the code-baked mark) — so a tenant that has never rebranded looks exactly like
// stock DeviceChain, with no round-trip color matching required.
package branding

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Branding is a white-labeling override. Every field is optional; a nil pointer
// inherits the next tier. The JSON shape is the wire form of the `branding.default`
// system setting, so the struct tags and the setting's code default (a JSON
// literal in settings/model.go) must agree.
type Branding struct {
	Title         *string `json:"title,omitempty"`
	Logo          *string `json:"logo,omitempty"`
	LogoMaxHeight *int    `json:"logoMaxHeight,omitempty"`
	Primary       *string `json:"primary,omitempty"`
	Background    *string `json:"background,omitempty"`
	Foreground    *string `json:"foreground,omitempty"`
	Accent        *string `json:"accent,omitempty"`
}

// Merge overlays a higher-priority override onto a lower one, field by field: a
// non-nil field in high wins, otherwise low's field carries. Used to fold a
// tenant's override over the operator/code default (branding.Merge(tenant,
// systemDefault)).
func Merge(high, low Branding) Branding {
	out := low
	if high.Title != nil {
		out.Title = high.Title
	}
	if high.Logo != nil {
		out.Logo = high.Logo
	}
	if high.LogoMaxHeight != nil {
		out.LogoMaxHeight = high.LogoMaxHeight
	}
	if high.Primary != nil {
		out.Primary = high.Primary
	}
	if high.Background != nil {
		out.Background = high.Background
	}
	if high.Foreground != nil {
		out.Foreground = high.Foreground
	}
	if high.Accent != nil {
		out.Accent = high.Accent
	}
	return out
}

// Validation bounds (ADR-038 §1.1 / §4). These govern an override at the mint
// point — invalid input is rejected server-side (fail-closed), never stored.
const (
	// MaxLogoDecodedBytes caps an inline data: logo on its DECODED bytes (base64
	// inflates ~33%, so a string-length check would be wrong). Keeps the
	// control-plane row and the cached tenant payload small (ADR-038 §1.1 Tier 0).
	MaxLogoDecodedBytes = 256 * 1024
	// MaxLogoURLLen bounds an https logo URL.
	MaxLogoURLLen = 2048
	// MaxTitleLen bounds the app title (tab + in-console product name).
	MaxTitleLen = 64
	// MinLogoHeight / MaxLogoHeight bound the chip/sidebar logo render height in px.
	MinLogoHeight = 16
	MaxLogoHeight = 200

	// LogoBlobPurpose is the object-store purpose segment for an uploaded logo
	// (ADR-058 key {instanceId}/{tenant}/branding-logo/{id}).
	LogoBlobPurpose = "branding-logo"
	// MaxUploadedLogoBytes caps a Tier-1 (object-store) uploaded logo. It is larger
	// than the Tier-0 inline cap (real storage, not a Postgres row) but still a
	// logo, not a background — larger login backgrounds are ADR-038 Phase 3.
	MaxUploadedLogoBytes = 1 << 20 // 1 MiB
)

// UploadableLogoMIME is the raster-only allow-list for an OBJECT-STORE (Tier-1)
// uploaded logo, mapping each accepted content type to its stored file extension.
// It mirrors the inline allow-list's raster-only stance for the same reason: SVG is
// a script/XSS carrier and stays https-only (rendered exclusively via <img src>).
// The extension is what makes the filesystem backend infer the right Content-Type
// on read, and the read proxy serves exactly these types.
var UploadableLogoMIME = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
}

var (
	hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	// dataImageRe captures the MIME of a base64 data: image URI. The base64 flag is
	// required — a non-base64 data: logo is rejected (we validate decoded size).
	dataImageRe = regexp.MustCompile(`^data:(image/[a-zA-Z0-9.+-]+);base64,`)
)

// allowedInlineMIME is the raster-only allow-list for an inline data: logo. SVG is
// deliberately excluded from the data: path — an inline data:image/svg+xml is a
// script/XSS carrier if ever rendered outside an <img>, and it is the one asset we
// cannot cheaply sanitize. SVG is allowed only as an external https URL, rendered
// exclusively via <img src> (SVG-in-<img> cannot execute script). See ADR-038 §1.1.
var allowedInlineMIME = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
}

// Validate rejects a malformed override (ADR-038 §4). All fields are optional; a
// nil field is a no-op (it clears/inherits). Contrast is intentionally NOT enforced
// here — a hard WCAG gate risks rejecting a legitimate brand color; the console
// shows a non-blocking contrast hint instead.
func Validate(b Branding) error {
	if b.Title != nil {
		if err := validateTitle(*b.Title); err != nil {
			return err
		}
	}
	for name, v := range map[string]*string{
		"primary": b.Primary, "background": b.Background,
		"foreground": b.Foreground, "accent": b.Accent,
	} {
		if v != nil && !hexColorRe.MatchString(*v) {
			return fmt.Errorf("branding.%s must be a hex color like #1f9fb7 (got %q)", name, *v)
		}
	}
	if b.Logo != nil {
		if err := validateLogo(*b.Logo); err != nil {
			return err
		}
	}
	if b.LogoMaxHeight != nil {
		if *b.LogoMaxHeight < MinLogoHeight || *b.LogoMaxHeight > MaxLogoHeight {
			return fmt.Errorf("branding.logoMaxHeight must be between %d and %d px (got %d)", MinLogoHeight, MaxLogoHeight, *b.LogoMaxHeight)
		}
	}
	return nil
}

func validateTitle(title string) error {
	if utf8.RuneCountInString(title) > MaxTitleLen {
		return fmt.Errorf("branding.title must be at most %d characters", MaxTitleLen)
	}
	for _, r := range title {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("branding.title must not contain control characters")
		}
	}
	return nil
}

// validateLogo enforces the ADR-038 §1.1 Tier-0 rules: an https URL (any image the
// browser renders in <img>) or a raster-only base64 data: URI within the decoded
// size ceiling. Everything else — http, an SVG data: URI, an over-ceiling image —
// is rejected fail-closed.
func validateLogo(logo string) error {
	switch {
	case strings.HasPrefix(logo, "https://"):
		if len(logo) > MaxLogoURLLen {
			return fmt.Errorf("branding.logo URL must be at most %d characters", MaxLogoURLLen)
		}
		u, err := url.Parse(logo)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("branding.logo must be a valid https URL")
		}
		return nil
	case strings.HasPrefix(logo, "data:"):
		m := dataImageRe.FindStringSubmatch(logo)
		if m == nil {
			return fmt.Errorf("branding.logo data: URI must be a base64-encoded image")
		}
		if _, ok := allowedInlineMIME[m[1]]; !ok {
			return fmt.Errorf("branding.logo inline images must be png, jpeg, or webp (got %q; SVG must be an https URL)", m[1])
		}
		payload := logo[len(m[0]):]
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return fmt.Errorf("branding.logo data: URI is not valid base64")
		}
		if len(decoded) > MaxLogoDecodedBytes {
			return fmt.Errorf("branding.logo must be at most %d KB (decoded)", MaxLogoDecodedBytes/1024)
		}
		return nil
	default:
		return fmt.Errorf("branding.logo must be an https URL or a base64 data:image/* URI")
	}
}
