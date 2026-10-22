// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Known-answer vector: pins the HKDF info, the key id label, the tenant-NUL-token input
// order and the encoding. Changing any of them changes every stored access token digest,
// which makes every token device in every instance stop authenticating, so it must be a
// deliberate new version. Computed outside Go (RFC 5869 HKDF and HMAC-SHA-256 with
// Python's hmac module).
const knownTokenDigest = "dctk1$45e11a26$Wn9Qm8yvWUJELiwuL4DDAWK5TdwDzkngfAX77QnGEJs"

func TestAccessTokenKnownAnswer(t *testing.T) {
	k := testDeviceKey(t)
	got, err := k.AccessTokenDigest("acme", "tok-0123456789")
	require.NoError(t, err)
	assert.Equal(t, knownTokenDigest, got)
	require.NoError(t, credential.VerifyAccessToken(k, "acme", knownTokenDigest, "tok-0123456789"))
}

// The digest is deterministic, because a lookup is an equality on it, and it carries no
// part of the token.
func TestAccessTokenDigestIsDeterministicAndOpaque(t *testing.T) {
	k := testDeviceKey(t)
	a, err := k.AccessTokenDigest("acme", "correct horse battery staple")
	require.NoError(t, err)
	b, err := k.AccessTokenDigest("acme", "correct horse battery staple")
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.NotContains(t, a, "horse")
	assert.True(t, strings.HasPrefix(a, k.AccessTokenDigestPrefix()), a)
	assert.True(t, credential.IsAccessTokenDigest(a))
	require.NoError(t, k.RecognizesAccessToken(a))

	// Distinct tokens, and the same token in distinct tenants, have distinct digests.
	c, err := k.AccessTokenDigest("acme", "correct horse battery stapl3")
	require.NoError(t, err)
	d, err := k.AccessTokenDigest("beta", "correct horse battery staple")
	require.NoError(t, err)
	assert.NotEqual(t, a, c)
	assert.NotEqual(t, a, d)
}

// A digest verifies the token it was made from and nothing else, byte for byte, in the
// tenant it was made for.
func TestAccessTokenDigestVerifiesOnlyItsTokenInItsTenant(t *testing.T) {
	k := testDeviceKey(t)
	d, err := k.AccessTokenDigest("acme", "tok")
	require.NoError(t, err)
	require.NoError(t, credential.VerifyAccessToken(k, "acme", d, "tok"))
	for _, wrong := range []string{"", "TOK", "tok ", " tok", "to", "tok\x00", d} {
		require.ErrorIs(t, credential.VerifyAccessToken(k, "acme", d, wrong), credential.ErrMismatch, "presented %q", wrong)
	}
	require.ErrorIs(t, credential.VerifyAccessToken(k, "beta", d, "tok"), credential.ErrMismatch,
		"a digest copied into another tenant's row must verify nothing there")
	require.ErrorIs(t, credential.VerifyAccessToken(k, "", d, "tok"), credential.ErrMismatch)
	require.ErrorIs(t, credential.VerifyAccessToken(nil, "acme", d, "tok"), credential.ErrMismatch, "no key fails closed")
	require.ErrorIs(t, credential.VerifyAccessToken(k, "acme", d, strings.Repeat("x", credential.MaxAccessTokenBytes+1)), credential.ErrMismatch)
}

// A stored value that is not a digest this key made is refused as unrecognized and never
// matches: malformed, another version, another key, a plaintext token, and a device MQTT
// secret digest, which is a different format under a different key.
func TestAccessTokenUnrecognizedStoredValues(t *testing.T) {
	k := testDeviceKey(t)
	other, err := credential.DeriveDeviceSecretKey(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	foreign, err := other.AccessTokenDigest("acme", "tok")
	require.NoError(t, err)
	good, err := k.AccessTokenDigest("acme", "tok")
	require.NoError(t, err)
	mqtt, err := k.Digest("acme", "tok")
	require.NoError(t, err)
	parts := strings.Split(good, "$")

	for name, stored := range map[string]string{
		"empty":                  "",
		"the plaintext token":    "tok",
		"foreign key":            foreign,
		"another version":        strings.Join([]string{"dctk2", parts[1], parts[2]}, "$"),
		"truncated mac":          strings.Join([]string{parts[0], parts[1], parts[2][:20]}, "$"),
		"not base64":             strings.Join([]string{parts[0], parts[1], "!!!"}, "$"),
		"missing part":           strings.Join(parts[:2], "$"),
		"an extra part":          good + "$x",
		"a device secret digest": mqtt,
	} {
		require.ErrorIs(t, k.RecognizesAccessToken(stored), credential.ErrAccessTokenUnrecognized, name)
		require.ErrorIs(t, credential.VerifyAccessToken(k, "acme", stored, "tok"), credential.ErrMismatch, name)
	}
	assert.True(t, credential.IsAccessTokenDigest(foreign), "a digest of any key has the digest shape")
	assert.False(t, credential.IsAccessTokenDigest("tok"))
	assert.False(t, credential.IsAccessTokenDigest("dctk1"), "the version marker alone is not a digest")
}

func TestAccessTokenDigestRefusals(t *testing.T) {
	k := testDeviceKey(t)
	_, err := k.AccessTokenDigest("", "tok")
	require.Error(t, err)
	_, err = k.AccessTokenDigest("acme", "")
	require.Error(t, err, "no token has no digest")
	_, err = k.AccessTokenDigest("acme", strings.Repeat("x", credential.MaxAccessTokenBytes+1))
	require.Error(t, err)
	_, err = k.AccessTokenDigest("acme", strings.Repeat("x", credential.MaxAccessTokenBytes))
	require.NoError(t, err)
}

// The access token key and the device secret key are separate keys derived from one root,
// so a digest of one kind cannot be presented as the other. This is the guard on the
// domain separation: the same bytes digested as an MQTT password and as an access token
// share no MAC.
func TestAccessTokenKeyIsSeparateFromTheDeviceSecretKey(t *testing.T) {
	k := testDeviceKey(t)
	token, err := k.AccessTokenDigest("acme", "same-bytes")
	require.NoError(t, err)
	secret, err := k.Digest("acme", "same-bytes")
	require.NoError(t, err)
	tokenMac := strings.Split(token, "$")[2]
	secretMac := strings.Split(secret, "$")[3]
	assert.NotEqual(t, tokenMac, secretMac)
	require.ErrorIs(t, credential.VerifyDeviceSecret(k, "acme", token, "same-bytes"), credential.ErrMismatch)
}

// 🔴 THE COMPARE IS CONSTANT TIME OVER FIXED-WIDTH INPUTS: whatever is stored, and whatever
// is presented, the two arguments of every constant-time compare have the same length, so
// neither the stored value's nor the token's shape shows in the timing.
func TestAccessTokenCompareInputsAreAlwaysTheSameWidth(t *testing.T) {
	k := testDeviceKey(t)
	good, err := k.AccessTokenDigest("acme", "tok")
	require.NoError(t, err)
	var widths [][2]int
	restore := credential.RecordConstantTimeCompareLengths(func(a, b int) { widths = append(widths, [2]int{a, b}) })
	defer restore()

	for _, stored := range []string{good, "", "tok", good[:10], good + "extra"} {
		for _, presented := range []string{"tok", "tok-longer-than-the-first", ""} {
			_ = credential.VerifyAccessToken(k, "acme", stored, presented)
		}
	}
	require.NotEmpty(t, widths, "no compare was made")
	for _, w := range widths {
		require.Equal(t, w[0], w[1], "a constant-time compare was handed inputs of different lengths")
	}
}
