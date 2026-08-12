// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmask

import (
	"testing"
	"time"
)

// 🔴 Found by a differential review against tokens.ts, and the behaviour was the
// exact inverse of what the code claimed. An overflowing width was treated as
// ABSENT, so {alphanumeric-99999999999999999999} fell back to the default width
// of 8, minted "abcdefgh", and was ACCEPTED — while the JS side parses that width
// as 1e20 and Array.from throws. A stored mask like that crashed every console
// surface that touched it: the settings editor on render, and the create form for
// that entity type. A comment claimed the length bound in Validate would catch
// it; the length bound never saw it, because the sample was 8 characters long.
func TestAnOverflowingWidthIsRefusedRatherThanTreatedAsAbsent(t *testing.T) {
	for _, mask := range []string{
		"{alphanumeric-99999999999999999999}",
		"{numeric-9223372036854775808}",
	} {
		if err := Validate(mask); err == nil {
			t.Errorf("Validate(%q) = nil, want an error (it samples as %q)", mask, Sample(mask))
		}
	}
}

// The bound is MaxTokenLen, and it is checked BEFORE sampling. The ordering is
// the whole point: minting a sample and letting the length check refuse it makes
// the refusal cost LINEAR IN THE NUMBER THE OPERATOR TYPED, which turns this
// validator into a resource-exhaustion vector on the settings write path — 1.66GB
// allocated for one ~20-byte mask declaring 1e8, measured.
//
// The ordering is asserted by TIMING, because it is not otherwise observable: a
// correct implementation and a catastrophic one return the same error.
func TestAnAbsurdWidthIsRefusedWithoutBuildingTheString(t *testing.T) {
	const mask = "{numeric-1000000000}"
	done := make(chan error, 1)
	go func() { done <- Validate(mask) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Validate(%q) = nil, want an error", mask)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Validate(%q) did not return promptly — it is sampling the declared width", mask)
	}
	// Sample is exported, so it must be safe on its own rather than merely
	// unreachable from Validate.
	if got := Sample(mask); got != "" {
		t.Errorf("Sample(%q) built %d characters; a width past the bound must contribute nothing", mask, len(got))
	}
}

// The counterweight: the bound must not refuse a width that CAN mint a legal
// token. Without this, refusing everything would pass the two tests above.
func TestWidthsUpToTheTokenLengthBoundAreAccepted(t *testing.T) {
	if err := Validate("{numeric-128}"); err != nil {
		t.Errorf("a width of exactly MaxTokenLen must be accepted, got %v", err)
	}
	if err := Validate("{numeric-129}"); err == nil {
		t.Error("a width past MaxTokenLen must be refused")
	}
}

// 🔴 Normalize claims to mirror normalizeToken, and did not at two code points —
// in OPPOSITE directions, which is why neither showed up as "one side is
// stricter". JavaScript's \s includes U+FEFF and excludes U+0085; Go's
// unicode.IsSpace is the reverse on both.
//
// Neither can reach the server today (the only string it normalizes is a fixed
// sample seed), but these two functions claim to mirror each other, and a claim
// that is false in a corner is one nobody can rely on in the middle.
func TestNormalizeUsesJavaScriptsWhitespaceSetNotGos(t *testing.T) {
	for _, tc := range []struct{ in, want, why string }{
		{"a\u0085b", "ab", "U+0085 is a space to Go but NOT to JavaScript"},
		{"a\ufeffb", "a-b", "U+FEFF is a space to JavaScript but NOT to Go"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q — %s", tc.in, got, tc.want, tc.why)
		}
	}
}
