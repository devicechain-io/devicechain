// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalEmailMapsEverySpellingToOne(t *testing.T) {
	want := "user@xn--bcher-kva.example"
	for _, in := range []string{
		"user@bücher.example",
		"user@xn--bcher-kva.example",
		"User@BÜCHER.Example",
		"USER@XN--BCHER-KVA.EXAMPLE",
		"  user@bücher.example.  ",
	} {
		got, err := CanonicalEmail(in)
		require.NoErrorf(t, err, "%q", in)
		require.Equalf(t, want, got, "%q", in)
		again, err := CanonicalEmail(got)
		require.NoError(t, err)
		require.Equal(t, got, again, "idempotent")
	}
	// ASCII addresses keep today's form: trimmed and lower-cased.
	got, err := CanonicalEmail(" Su@Example.COM ")
	require.NoError(t, err)
	require.Equal(t, "su@example.com", got)
	// "ß" is its own domain, not "ss".
	a, _ := CanonicalEmail("x@straße.example")
	b, _ := CanonicalEmail("x@strasse.example")
	require.NotEqual(t, a, b)
}

func TestCanonicalEmailRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, in := range []string{"", "a", "a@", "@b.example", "a@@b.example", "a@b@c.example",
		"a@xn--a.example", "a@b c.example", "a@b_c.example", "a@b.example..", "a@" + string([]byte{0xff}) + ".example"} {
		_, err := CanonicalEmail(in)
		require.ErrorIsf(t, err, ErrEmailInvalid, "%q", in)
	}
}
