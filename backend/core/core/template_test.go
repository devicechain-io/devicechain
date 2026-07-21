// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"regexp"
	"testing"
)

func TestRenderTemplateIndex(t *testing.T) {
	cases := []struct {
		name     string
		template string
		index    int
		want     string
	}{
		{"unpadded", "car-{n}", 7, "car-7"},
		{"padded width 4", "car-{n:04d}", 7, "car-0007"},
		{"padded width 4 large", "car-{n:04d}", 1234, "car-1234"},
		{"padded overflow keeps digits", "car-{n:02d}", 1234, "car-1234"},
		{"two placeholders", "{n}-of-{n:03d}", 5, "5-of-005"},
		{"no placeholder is literal", "just-a-name", 9, "just-a-name"},
		{"index only in middle", "v{n}x", 3, "v3x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderTemplate(tc.template, tc.index, nil)
			if got != tc.want {
				t.Fatalf("RenderTemplate(%q, %d) = %q, want %q", tc.template, tc.index, got, tc.want)
			}
		})
	}
}

// A template with no {random} must be a pure function of (template, index): the
// same inputs render identically every call. This is the property that lets a
// client predict the token the server will render for a given index.
func TestRenderTemplateIsDeterministicWithoutRandom(t *testing.T) {
	const tmpl = "fleet-{n:05d}"
	first := RenderTemplate(tmpl, 42, nil)
	for i := 0; i < 100; i++ {
		if got := RenderTemplate(tmpl, 42, nil); got != first {
			t.Fatalf("render %d = %q, want stable %q", i, got, first)
		}
	}
	if first != "fleet-00042" {
		t.Fatalf("unexpected render %q", first)
	}
}

// A nil randToken leaves {random} as a literal — it never silently drops the
// placeholder or panics.
func TestRenderTemplateRandomNilLeavesLiteral(t *testing.T) {
	got := RenderTemplate("vin-{random}", 1, nil)
	if got != "vin-{random}" {
		t.Fatalf("nil randToken: got %q, want the literal placeholder preserved", got)
	}
}

// Each {random} occurrence calls randToken once, so N occurrences yield N
// independent substitutions (not one value reused).
func TestRenderTemplateRandomPerOccurrence(t *testing.T) {
	n := 0
	seq := []string{"aaaa", "bbbb", "cccc"}
	next := func() string {
		v := seq[n]
		n++
		return v
	}
	got := RenderTemplate("{random}-{n}-{random}", 5, next)
	if got != "aaaa-5-bbbb" {
		t.Fatalf("got %q, want %q", got, "aaaa-5-bbbb")
	}
	if n != 2 {
		t.Fatalf("randToken called %d times, want 2", n)
	}
}

func TestHasIndexPlaceholder(t *testing.T) {
	cases := map[string]bool{
		"car-{n}":      true,
		"car-{n:04d}":  true,
		"vin-{random}": false, // random varies but is not the per-index guarantee
		"fixed-name":   false,
		"{n}{random}":  true,
		"":             false,
	}
	for tmpl, want := range cases {
		if got := HasIndexPlaceholder(tmpl); got != want {
			t.Errorf("HasIndexPlaceholder(%q) = %v, want %v", tmpl, got, want)
		}
	}
}

func TestValidateTemplate(t *testing.T) {
	// Fine: bounded length, no or small pad widths.
	for _, ok := range []string{"car-{n}", "car-{n:04d}", "vin-{random}", "no-placeholders", ""} {
		if err := ValidateTemplate(ok); err != nil {
			t.Errorf("ValidateTemplate(%q) = %v, want nil", ok, err)
		}
	}
	// Too long.
	if err := ValidateTemplate(string(make([]byte, MaxTemplateLen+1))); err == nil {
		t.Error("an over-length template must be rejected")
	}
	// Memory-amplifying pad width.
	if err := ValidateTemplate("x-{n:01000000d}"); err == nil {
		t.Error("a pad width above the maximum must be rejected")
	}
	// At the boundary width is allowed.
	if err := ValidateTemplate("x-{n:0128d}"); err != nil {
		t.Errorf("width == MaxTemplatePadWidth must be allowed, got %v", err)
	}
}

// Even if a caller skips ValidateTemplate, RenderTemplate itself must never render
// a memory-amplifying string: the width is clamped defensively.
func TestRenderTemplateClampsHugeWidth(t *testing.T) {
	got := RenderTemplate("x-{n:09999999d}", 7, nil)
	if len(got) > MaxTemplatePadWidth+2 { // "x-" + at most MaxTemplatePadWidth digits
		t.Fatalf("render length %d exceeds the clamp; width was not bounded", len(got))
	}
}

// RandomHexToken must always satisfy the token grammar (it fills externalId
// templates, which may themselves be tokens) and must actually vary.
func TestRandomHexToken(t *testing.T) {
	hexRe := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		tok := RandomHexToken()
		if !hexRe.MatchString(tok) {
			t.Fatalf("RandomHexToken() = %q, not 16 lowercase hex chars", tok)
		}
		if err := ValidateToken(tok); err != nil {
			t.Fatalf("RandomHexToken() = %q fails ValidateToken: %v", tok, err)
		}
		if seen[tok] {
			t.Fatalf("RandomHexToken() collided on %q within 1000 draws", tok)
		}
		seen[tok] = true
	}
}
