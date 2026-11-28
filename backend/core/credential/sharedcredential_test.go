// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"errors"
	"strings"
	"testing"
)

func TestMatchSharedCredential(t *testing.T) {
	a := strings.Repeat("a", 43)
	b := strings.Repeat("b", 43)
	short := strings.Repeat("s", MinSharedCredentialLen-1)
	cases := []struct {
		name       string
		presented  string
		configured []ConfiguredCredential
		want       string
		usable     int
		err        error
	}{
		{"the matching caller is named", b, []ConfiguredCredential{{"x", a}, {"y", b}}, "y", 2, nil},
		{"no match", a + "z", []ConfiguredCredential{{"x", a}, {"y", b}}, "", 2, nil},
		{"a prefix does not match", a[:40], []ConfiguredCredential{{"x", a}}, "", 1, nil},
		{"empty matches nothing, not even an empty configured value", "", []ConfiguredCredential{{"x", ""}}, "", 0, nil},
		{"a short configured value is no credential, even presented exactly", short, []ConfiguredCredential{{"x", short}, {"y", b}}, "", 1, nil},
		{"none configured", a, nil, "", 0, nil},
		// Two callers given the same value: refused, never resolved to either (first or sorted).
		{"ambiguous", a, []ConfiguredCredential{{"y", a}, {"x", a}}, "", 2, ErrAmbiguousCredential},
		{"ambiguous behind a distinct one", a, []ConfiguredCredential{{"z", b}, {"x", a}, {"y", a}}, "", 3, ErrAmbiguousCredential},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, usable, err := MatchSharedCredential(tc.presented, tc.configured)
			if got != tc.want || usable != tc.usable || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("got (%q, %d, %v), want (%q, %d, %v)", got, usable, err, tc.want, tc.usable, tc.err)
			}
		})
	}
}

// Every configured credential is compared, even after a match: no early exit.
func TestMatchSharedCredentialComparesEveryConfiguredValue(t *testing.T) {
	calls := 0
	prev := constantTimeCompare
	t.Cleanup(func() { constantTimeCompare = prev })
	constantTimeCompare = func(x, y []byte) int {
		calls++
		return prev(x, y)
	}
	a := strings.Repeat("a", 43)
	cfg := []ConfiguredCredential{{"x", a}, {"y", strings.Repeat("b", 43)}, {"z", strings.Repeat("c", 43)}}
	if got, _, _ := MatchSharedCredential(a, cfg); got != "x" {
		t.Fatalf("got %q", got)
	}
	if calls != 3 {
		t.Fatalf("compared %d of 3 configured credentials", calls)
	}
}
