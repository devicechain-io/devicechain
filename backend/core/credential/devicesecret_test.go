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

// A digest verifies the secret it was made from and nothing else, byte for byte.
func TestDeviceSecretDigestVerifiesOnlyItsSecret(t *testing.T) {
	k := testDeviceKey(t)
	d, err := k.Digest("s3cret")
	require.NoError(t, err)
	require.NoError(t, credential.VerifyDeviceSecret(k, d, "s3cret"))
	for _, wrong := range []string{"", "s3creT", "s3cret ", " s3cret", "s3cre", "s3cret\x00"} {
		require.ErrorIs(t, credential.VerifyDeviceSecret(k, d, wrong), credential.ErrMismatch, "presented %q", wrong)
	}
}

// The stored form carries no part of the secret, and two digests of one secret differ
// (the salt), so a reader of the table cannot see which devices share a password.
func TestDeviceSecretDigestIsSaltedAndOpaque(t *testing.T) {
	k := testDeviceKey(t)
	a, err := k.Digest("correct horse battery staple")
	require.NoError(t, err)
	b, err := k.Digest("correct horse battery staple")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	require.NoError(t, credential.VerifyDeviceSecret(k, a, "correct horse battery staple"))
	require.NoError(t, credential.VerifyDeviceSecret(k, b, "correct horse battery staple"))
	for _, d := range []string{a, b} {
		assert.NotContains(t, d, "horse")
		assert.True(t, strings.HasPrefix(d, "v1$"+k.KeyId()+"$"), d)
	}
}

// Known-answer vector: pins the HKDF info, the key id label, the salt-then-secret order and
// the encoding. Changing any of them changes every stored digest, which makes every
// device password in every instance stop working, so it must be a deliberate new version.
func TestDeviceSecretKnownAnswer(t *testing.T) {
	k := testDeviceKey(t)
	salt := bytes.Repeat([]byte{0xA5}, 16)
	got := credential.DigestWithSalt(k, salt, "s3cret")
	assert.Equal(t, knownDigest, got)
	require.NoError(t, credential.VerifyDeviceSecret(k, knownDigest, "s3cret"))
}

// Computed outside Go (RFC 5869 HKDF and HMAC-SHA-256 with Python's hmac module).
const knownDigest = "v1$45e11a26$paWlpaWlpaWlpaWlpaWlpQ$IklfTBiHxaIdmgfcwJ5cuc85xlf4PNivyYqrqtXdZQU"

// A digest is refused as unrecognized — and never matches — when it is malformed, of
// another version, or made under another root key. The last is what a database restored
// next to the wrong root key looks like, and it must not read as a wrong password.
func TestDeviceSecretUnrecognizedDigests(t *testing.T) {
	k := testDeviceKey(t)
	other, err := credential.DeriveDeviceSecretKey(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	require.NotEqual(t, k.KeyId(), other.KeyId())
	foreign, err := other.Digest("s3cret")
	require.NoError(t, err)
	good, err := k.Digest("s3cret")
	require.NoError(t, err)
	parts := strings.Split(good, "$")

	for name, stored := range map[string]string{
		"foreign key":     foreign,
		"plaintext":       "s3cret",
		"empty":           "",
		"other version":   "v2$" + strings.Join(parts[1:], "$"),
		"short salt":      strings.Join([]string{parts[0], parts[1], parts[2][:10], parts[3]}, "$"),
		"short mac":       strings.Join([]string{parts[0], parts[1], parts[2], parts[3][:20]}, "$"),
		"bad base64":      strings.Join([]string{parts[0], parts[1], "!!!!", parts[3]}, "$"),
		"extra field":     good + "$x",
		"missing a field": strings.Join(parts[:3], "$"),
	} {
		require.ErrorIs(t, k.Recognizes(stored), credential.ErrDeviceSecretUnrecognized, name)
		require.ErrorIs(t, credential.VerifyDeviceSecret(k, stored, "s3cret"), credential.ErrMismatch, name)
	}
	require.NoError(t, k.Recognizes(good))
}

// 🔴 THE COMPARE IS CONSTANT-TIME OVER FIXED-WIDTH INPUTS. Every Verify — a match, a
// mismatch of a different length, and an unrecognized stored value — reaches the
// constant-time compare with two 32-byte MACs, so the time a compare takes depends on
// neither secret's length nor on how far the two agree. ConstantTimeCompare returns at
// once on a length difference, which is why the widths are what is asserted.
func TestDeviceSecretVerifyComparesFixedWidthInConstantTime(t *testing.T) {
	k := testDeviceKey(t)
	d, err := k.Digest("short")
	require.NoError(t, err)

	var lengths [][2]int
	restore := credential.RecordConstantTimeCompareLengths(func(a, b int) { lengths = append(lengths, [2]int{a, b}) })
	defer restore()

	require.NoError(t, credential.VerifyDeviceSecret(k, d, "short"))
	require.ErrorIs(t, credential.VerifyDeviceSecret(k, d, "a much longer presented secret than the stored one"), credential.ErrMismatch)
	require.ErrorIs(t, credential.VerifyDeviceSecret(k, d, ""), credential.ErrMismatch)
	require.ErrorIs(t, credential.VerifyDeviceSecret(k, "garbage", "short"), credential.ErrMismatch)
	assert.Equal(t, [][2]int{{32, 32}, {32, 32}, {32, 32}, {32, 32}}, lengths,
		"every Verify must reach the constant-time compare with two 32-byte inputs")
}

// Digest refuses what must never be stored as a digest: nothing ("no secret" is NULL), and
// a secret over the limit the plaintext column used to enforce.
func TestDeviceSecretDigestRefusesEmptyAndOverlong(t *testing.T) {
	k := testDeviceKey(t)
	_, err := k.Digest("")
	require.Error(t, err)
	_, err = k.Digest(strings.Repeat("x", credential.MaxDeviceSecretBytes+1))
	require.Error(t, err)
	_, err = k.Digest(strings.Repeat("x", credential.MaxDeviceSecretBytes))
	require.NoError(t, err)
}

// The key is derived from a 256-bit root key only.
func TestDeriveDeviceSecretKeyNeedsA32ByteRootKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		_, err := credential.DeriveDeviceSecretKey(make([]byte, n))
		require.Error(t, err, "%d bytes", n)
	}
}
