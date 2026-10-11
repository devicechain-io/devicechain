// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeDomain(t *testing.T) {
	for in, want := range map[string]string{
		"ExAmple.COM":           "example.com",
		"  example.com  ":       "example.com",
		"example.com.":          "example.com",
		"mail.Example.Com":      "mail.example.com",
		"bücher.example":        "xn--bcher-kva.example",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"BÜCHER.example":        "xn--bcher-kva.example",
	} {
		got, err := NormalizeDomain(in)
		require.NoErrorf(t, err, "%q", in)
		require.Equalf(t, want, got, "%q", in)
	}

	// An internationalised name and its punycode are ONE domain, or the uniqueness that
	// keeps a domain with a single provider is bypassed by spelling.
	a, err := NormalizeDomain("bücher.example")
	require.NoError(t, err)
	b, err := NormalizeDomain("xn--bcher-kva.example")
	require.NoError(t, err)
	require.Equal(t, a, b)

	for _, bad := range []string{
		"", "   ", ".", "localhost", "user@example.com", "https://example.com", "example.com/path",
		"exa mple.com", "1.2.3.4", "-bad.example", "a..b.example", "example.com:443",
		"example.com..", // the profile keeps a second trailing dot, which would be a second spelling
	} {
		_, err := NormalizeDomain(bad)
		require.ErrorIsf(t, err, ErrDomainInvalid, "%q", bad)
	}
}

func TestValidateDomainRefusesFreemailAndPublicSuffix(t *testing.T) {
	for _, d := range []string{"gmail.com", "GMAIL.com", "outlook.com", "hotmail.com", "yahoo.com", "icloud.com", "proton.me",
		"mail.yahoo.com" /* a subdomain of a freemail domain */, "hotmail.co.uk"} {
		_, err := ValidateDomain(d)
		require.ErrorIsf(t, err, ErrDomainFreemail, "%q", d)
	}
	for _, d := range []string{"co.uk", "com", "uk", "github.io", "CO.UK", "blogspot.com"} {
		_, err := ValidateDomain(d)
		require.Errorf(t, err, "%q", d)
	}
	_, err := ValidateDomain("co.uk")
	require.ErrorIs(t, err, ErrDomainPublicSuffix)

	// The controls: ordinary organisation domains pass, normalised, so the refusals above
	// are rules and not a validator that refuses everything.
	for in, want := range map[string]string{
		"ExAmple.COM":            "example.com",
		"acme.co.uk":             "acme.co.uk",
		"eng.acme.com":           "eng.acme.com",
		"xn--bcher-kva.example":  "xn--bcher-kva.example",
		"bücher.example":         "xn--bcher-kva.example",
		"someone.github.io":      "someone.github.io", // below a private suffix its owner controls
		"gmail.com.acme.example": "gmail.com.acme.example",
		"notgmail.com":           "notgmail.com",
	} {
		got, err := ValidateDomain(in)
		require.NoErrorf(t, err, "%q", in)
		require.Equalf(t, want, got, "%q", in)
	}
}
