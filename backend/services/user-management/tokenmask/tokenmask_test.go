// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tokenmask

import (
	"strings"
	"testing"
)

// 🔴 The cases in this table were produced by RUNNING the console's generator
// (frontend/packages/client/src/tokens.ts) with a zero-returning RNG and a fixed
// uuid, not by reading it. Two of them are counter-intuitive enough that reading
// would have got them wrong:
//
//	{alphanumeric-0}  mints ""      — "0" is a truthy string in JS, so the parsed
//	                                  0 survives `seg.n ?? 8`
//	dev-{sulg}        mints "dev-"  — an unknown placeholder contributes NOTHING
//
// This table is the cross-language pin. If tokens.ts changes, these fail; that is
// the point, since nothing else connects the two implementations.
func TestSampleMirrorsTheConsoleGenerator(t *testing.T) {
	// The console fills {alphanumeric}/{numeric} from a random index; with
	// random() == 0 it picks the alphabet's first character every time. Sample
	// walks the alphabet in order instead, so only the LENGTH is comparable for
	// those two — asserted separately below.
	for _, tc := range []struct{ mask, want string }{
		{"{alphanumeric-0}", ""},
		{"dev-{sulg}", "dev-"},
		{"device", "device"},
		{"a-{slug}", "a-sample-name"},
		{"{SLUG}", "sample-name"},
	} {
		if got := Sample(tc.mask); got != tc.want {
			t.Errorf("Sample(%q) = %q, want %q", tc.mask, got, tc.want)
		}
	}

	for _, tc := range []struct {
		mask string
		want int
	}{
		{"{alphanumeric}", 8},
		{"{alphanumeric-4}", 4},
		{"{numeric}", 4},
		{"{numeric-6}", 6},
	} {
		if got := len(Sample(tc.mask)); got != tc.want {
			t.Errorf("len(Sample(%q)) = %d, want %d", tc.mask, got, tc.want)
		}
	}

	// {uuid} mints a uuid-shaped value; the console's is random, ours is fixed, so
	// the shape is what carries over.
	if got := Sample("{uuid}"); len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Errorf("Sample(\"{uuid}\") = %q, want a uuid shape", got)
	}
}

func TestNormalizeMirrorsTheConsole(t *testing.T) {
	// Also produced by running normalizeToken, not by reading it.
	for _, tc := range []struct{ in, want string }{
		{"  Bay 12 __ x-- ", "bay-12-x"},
		{"North Yard", "north-yard"},
		{"Ops Overview", "ops-overview"},
		{"!!!", ""},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateRefusesMasksThatMintSilentlyBrokenTokens(t *testing.T) {
	for _, tc := range []struct{ mask, because string }{
		{"", "an empty mask mints nothing"},
		{"dev-{sulg}", "an unknown placeholder contributes nothing, minting \"dev-\""},
		{"device", "no placeholder — every entity would collide on the same token"},
		{"{alphanumeric-0}", "a zero width mints an empty token"},
		{"my device-{slug}", "a space in a literal is not in the token grammar"},
		{"-{slug}", "a token may not start with a hyphen"},
		{"{alphanumeric-500}", "the sample exceeds the maximum token length"},
		{"area/{slug}", "a slash is not in the token grammar"},
	} {
		if err := Validate(tc.mask); err == nil {
			t.Errorf("Validate(%q) = nil, want an error: %s (it mints %q)", tc.mask, tc.because, Sample(tc.mask))
		}
	}
}

// The counterweight: refusing bad masks is only worth anything while the masks
// operators actually use still pass. Every entry here is a mask from a shipped
// default or the settings description.
func TestValidateAcceptsUsableMasks(t *testing.T) {
	for _, mask := range []string{
		"{slug}",
		"device-{alphanumeric-4}",
		"area-{slug}",
		"pin-{numeric-4}",
		"{uuid}",
		"dev_{slug}",
		"{slug}-{numeric-2}",
	} {
		if err := Validate(mask); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", mask, err)
		}
	}
}

// The error has to name the placeholder that is wrong. An operator staring at a
// twenty-key mask map needs to be told which one, not that "a mask is invalid".
func TestUnknownPlaceholderErrorNamesIt(t *testing.T) {
	err := Validate("dev-{sulg}")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "{sulg}") {
		t.Errorf("error %q does not name the offending placeholder", err)
	}
}
