// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package locale

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ptr(s string) *string { return &s }

func TestValidateAcceptsTheTagShapesTheConsoleCanResolve(t *testing.T) {
	for _, tag := range []string{"en", "es", "pt-BR", "zh-Hans-CN", "es-419", "fil"} {
		require.NoErrorf(t, Validate(ptr(tag)), "%q is a well-formed language tag", tag)
	}
}

// A nil locale is what "inherit" looks like, so it must not be an error — otherwise
// the write path could never clear an override.
func TestValidateAcceptsNilBecauseThatIsInherit(t *testing.T) {
	require.NoError(t, Validate(nil))
}

func TestValidateRejectsWhatIsNotALanguageTag(t *testing.T) {
	for _, bad := range []string{
		"English",     // prose, not a tag
		"en_US",       // the POSIX spelling; i18next resolves it to nothing
		"e",           // too short to be a primary subtag
		"englishlang", // too long to be a primary subtag
		"en-",         // trailing separator
		"-en",         // leading separator
		"en US",       // a space is never a subtag separator
		"en-USA",      // a 3-letter region is not in the grammar (3 DIGITS is)
		"",            // a blank that skipped Normalize must be refused, not stored
	} {
		require.Errorf(t, Validate(ptr(bad)), "%q must be refused", bad)
	}
}

// The length bound is checked BEFORE the shape, so a kilobyte of text gets a legible
// refusal rather than being run through the regex.
func TestValidateRejectsAnOverlongValue(t *testing.T) {
	err := Validate(ptr(strings.Repeat("a", MaxLen+1)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "at most")
}

// 🔴 The blank-to-nil collapse. A pointer to "" (or to whitespace) is what a non-console
// client sends meaning "clear this", and a stored blank is NOT an absent one — it wins
// Merge and masks the operator's default.
func TestNormalizeCollapsesABlankToInherit(t *testing.T) {
	require.Nil(t, Normalize(ptr("")))
	require.Nil(t, Normalize(ptr("   ")))
	require.Nil(t, Normalize(nil))
}

func TestNormalizeTrimsAndCanonicalizesCase(t *testing.T) {
	for input, want := range map[string]string{
		"  es  ":      "es",
		"ES":          "es",
		"es-mx":       "es-MX",
		"PT-br":       "pt-BR",
		"zh-hans-cn":  "zh-Hans-CN",
		"\tes-419\n":  "es-419",
		"zh-HANS-cn ": "zh-Hans-CN",
	} {
		got := Normalize(ptr(input))
		require.NotNilf(t, got, "%q normalizes to a value", input)
		require.Equalf(t, want, *got, "%q", input)
	}
}

// Normalize must not mutate its caller's string through the pointer — the resolver
// hands it a value read off the tenant row.
func TestNormalizeDoesNotMutateTheInput(t *testing.T) {
	in := "  ES-mx "
	p := &in
	require.Equal(t, "es-MX", *Normalize(p))
	require.Equal(t, "  ES-mx ", in)
}

// 🔴 The tenant is the HIGH tier. A swapped argument order silently makes the
// operator's instance default win over every tenant's own choice, and nothing else in
// the package would notice.
func TestMergePrefersTheHighTier(t *testing.T) {
	got := Merge(ptr("es"), ptr("en"))
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

func TestMergeFallsThroughToTheLowTierWhenTheHighTierInherits(t *testing.T) {
	got := Merge(nil, ptr("en"))
	require.NotNil(t, got)
	require.Equal(t, "en", *got)
}

func TestMergeResolvesToNothingWhenNeitherTierSetsALocale(t *testing.T) {
	require.Nil(t, Merge(nil, nil))
}

// 🔴 The reason Merge normalizes rather than trusting its inputs: a blank at the high
// tier must NOT mask the tier below. Delete the Normalize call in Merge and this
// reddens with a resolved locale of "".
func TestMergeDoesNotLetABlankHighTierMaskTheDefault(t *testing.T) {
	got := Merge(ptr("   "), ptr("en"))
	require.NotNil(t, got, "a blank override must fall through, not resolve to blank")
	require.Equal(t, "en", *got)
}

func TestMergeCanonicalizesWhicheverTierWins(t *testing.T) {
	got := Merge(nil, ptr("PT-br"))
	require.NotNil(t, got)
	require.Equal(t, "pt-BR", *got)
}
