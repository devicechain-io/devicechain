// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testTransit(t *testing.T, fill byte) *SecretTransit {
	t.Helper()
	root := make([]byte, 32)
	for i := range root {
		root[i] = fill
	}
	tr, err := DeriveSecretTransit(root)
	require.NoError(t, err)
	return tr
}

func TestSecretTransitRoundTrip(t *testing.T) {
	tr := testTransit(t, 1)
	sealed, err := tr.Seal("acme", "sensor-1", "correct horse battery")
	require.NoError(t, err)
	require.NotContains(t, sealed, "correct horse")
	got, err := tr.Open("acme", "sensor-1", sealed)
	require.NoError(t, err)
	require.Equal(t, "correct horse battery", got)

	again, _ := tr.Seal("acme", "sensor-1", "correct horse battery")
	require.NotEqual(t, sealed, again, "a fresh nonce per seal")
}

func TestSecretTransitRefusesWhatWasNotSealedForThisCredential(t *testing.T) {
	tr := testTransit(t, 1)
	sealed, err := tr.Seal("acme", "sensor-1", "s3cret-value-long-enough")
	require.NoError(t, err)

	for name, c := range map[string]struct{ tenant, id, val string }{
		"another tenant":     {"other", "sensor-1", sealed},
		"another credential": {"acme", "sensor-2", sealed},
		"plaintext":          {"acme", "sensor-1", "s3cret-value-long-enough"},
		"empty":              {"acme", "sensor-1", ""},
		"truncated":          {"acme", "sensor-1", sealed[:len(sealed)-4]},
		"tampered":           {"acme", "sensor-1", sealed[:len(sealed)-2] + "AA"},
		"wrong version":      {"acme", "sensor-1", strings.Replace(sealed, "dcs1", "dcs2", 1)},
		"stored digest form": {"acme", "sensor-1", "v1$00000000$AAAA$BBBB"},
	} {
		_, err := tr.Open(c.tenant, c.id, c.val)
		require.ErrorIs(t, err, ErrSealedSecretInvalid, name)
	}
	_, err = testTransit(t, 2).Open("acme", "sensor-1", sealed)
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "another root key")
	var nilTransit *SecretTransit
	_, err = nilTransit.Open("acme", "sensor-1", sealed)
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "no key")
}

// The transit key and the digest key come from the same root key under different contexts.
// Compared as KEY MATERIAL, not ids: a value sealed under the digest key must not open.
func TestSecretTransitKeyIsSeparateFromTheDigestKey(t *testing.T) {
	root := make([]byte, 32)
	tr, err := DeriveSecretTransit(root)
	require.NoError(t, err)
	dk, err := DeriveDeviceSecretKey(root)
	require.NoError(t, err)

	transitKey, err := hkdf.Key(sha256.New, root, nil, secretTransitInfo, sha256.Size)
	require.NoError(t, err)
	require.NotEqual(t, transitKey, dk.key[:], "the two keys are the same bytes")

	// A transit built on the DIGEST key's bytes does not open what the real transit seals,
	// and the reverse.
	block, err := aes.NewCipher(dk.key[:])
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	wrong := &SecretTransit{aead: aead, id: tr.id}
	sealed, err := tr.Seal("acme", "c", "a-long-enough-secret")
	require.NoError(t, err)
	_, err = wrong.Open("acme", "c", sealed)
	require.ErrorIs(t, err, ErrSealedSecretInvalid)
	sealedWrong, err := wrong.Seal("acme", "c", "a-long-enough-secret")
	require.NoError(t, err)
	_, err = tr.Open("acme", "c", sealedWrong)
	require.ErrorIs(t, err, ErrSealedSecretInvalid)
}

func TestSecretTransitRefusesBadInput(t *testing.T) {
	_, err := DeriveSecretTransit(make([]byte, 16))
	require.Error(t, err)
	tr := testTransit(t, 1)
	for _, c := range [][3]string{{"", "a", "b"}, {"a", "", "b"}, {"a", "b", ""}} {
		_, err := tr.Seal(c[0], c[1], c[2])
		require.Error(t, err)
	}
}

func TestTokenTransitRoundTripAndBinding(t *testing.T) {
	tr := testTransit(t, 1)
	sealed, err := tr.SealToken("acme", "ACCESS_TOKEN", "tok-0123456789")
	require.NoError(t, err)
	require.NotContains(t, sealed, "tok-0123456789")
	got, err := tr.OpenToken("acme", "ACCESS_TOKEN", sealed)
	require.NoError(t, err)
	require.Equal(t, "tok-0123456789", got)

	for name, open := range map[string]func() (string, error){
		"another tenant": func() (string, error) { return tr.OpenToken("other", "ACCESS_TOKEN", sealed) },
		"another type":   func() (string, error) { return tr.OpenToken("acme", "MQTT_BASIC", sealed) },
		"as a password":  func() (string, error) { return tr.Open("acme", "tok-0123456789", sealed) },
		"another key":    func() (string, error) { return testTransit(t, 2).OpenToken("acme", "ACCESS_TOKEN", sealed) },
		"plaintext":      func() (string, error) { return tr.OpenToken("acme", "ACCESS_TOKEN", "tok-0123456789") },
		"empty":          func() (string, error) { return tr.OpenToken("acme", "ACCESS_TOKEN", "") },
	} {
		_, err := open()
		require.ErrorIs(t, err, ErrSealedSecretInvalid, name)
	}

	pw, err := tr.Seal("acme", "ACCESS_TOKEN", "a-password-value")
	require.NoError(t, err)
	_, err = tr.OpenToken("acme", "ACCESS_TOKEN", pw)
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "a sealed password is not a sealed token")

	for _, a := range [][3]string{{"", "ACCESS_TOKEN", "t"}, {"acme", "", "t"}, {"acme", "ACCESS_TOKEN", ""}} {
		_, err := tr.SealToken(a[0], a[1], a[2])
		require.Error(t, err)
	}
	again, _ := tr.SealToken("acme", "ACCESS_TOKEN", "tok-0123456789")
	require.NotEqual(t, sealed, again, "a fresh nonce per seal")
}
