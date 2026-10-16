// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/chacha20poly1305"
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
	require.False(t, bytes.Contains(sealed, []byte("correct horse")))
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

	wrongMagic := bytes.Clone(sealed)
	wrongMagic[0] ^= 0x01
	wrongId := bytes.Clone(sealed)
	wrongId[1] ^= 0x01
	for name, c := range map[string]struct {
		tenant, id string
		val        []byte
	}{
		"another tenant":     {"other", "sensor-1", sealed},
		"another credential": {"acme", "sensor-2", sealed},
		"plaintext":          {"acme", "sensor-1", []byte("s3cret-value-long-enough")},
		"empty":              {"acme", "sensor-1", nil},
		"header only":        {"acme", "sensor-1", sealed[:sealedHeaderN]},
		"truncated":          {"acme", "sensor-1", sealed[:len(sealed)-4]},
		"tampered":           {"acme", "sensor-1", tamperSealed(t, sealed)},
		"wrong magic":        {"acme", "sensor-1", wrongMagic},
		"wrong key id":       {"acme", "sensor-1", wrongId},
		"text form unread": {"acme", "sensor-1",
			[]byte(legacyTextSeal(t, tr.aead, tr, "s3cret-value-long-enough", transitAAD("acme", "sensor-1")))},
		"stored digest form": {"acme", "sensor-1", []byte("v1$00000000$AAAA$BBBB")},
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

// tamperSealed flips one bit of the sealed value's first ciphertext byte.
func tamperSealed(t *testing.T, sealed []byte) []byte {
	t.Helper()
	require.Greater(t, len(sealed), sealedHeaderN+chacha20poly1305.NonceSizeX)
	tampered := bytes.Clone(sealed)
	tampered[sealedHeaderN+chacha20poly1305.NonceSizeX] ^= 0x01
	return tampered
}

// legacyTextSeal is the seal as producers wrote it before the binary form, copied from that
// code: dcs2$<key id, 8 hex>$<nonce, ciphertext and tag, base64 raw std>. Messages in this
// form can still be on a stream across an upgrade.
func legacyTextSeal(t *testing.T, aead cipher.AEAD, tr *SecretTransit, plain string, aad []byte) string {
	t.Helper()
	nonce := make([]byte, aead.NonceSize())
	_, err := rand.Read(nonce)
	require.NoError(t, err)
	sealed := aead.Seal(nonce, nonce, []byte(plain), aad)
	return strings.Join([]string{"dcs2", hex.EncodeToString(tr.id[:]), base64.RawStdEncoding.EncodeToString(sealed)}, "$")
}

// A seal written in the text form, by a producer from before the binary form, still opens
// once SealedFromText has brought it over, for both purposes; and the binary form is what
// it is claimed to be: a third smaller than the text one for a 32-character token.
func TestSealedFromTextOpensWhatTheTextFormSealed(t *testing.T) {
	tr := testTransit(t, 1)
	token := "0123456789abcdef0123456789abcdef"
	text := legacyTextSeal(t, tr.tokenAEAD, tr, token, tokenAAD("acme", "ACCESS_TOKEN"))
	got, err := tr.OpenToken("acme", "ACCESS_TOKEN", SealedFromText(text))
	require.NoError(t, err)
	require.Equal(t, token, got)

	pwText := legacyTextSeal(t, tr.aead, tr, "a-password-value", transitAAD("acme", "c1"))
	pw, err := tr.Open("acme", "c1", SealedFromText(pwText))
	require.NoError(t, err)
	require.Equal(t, "a-password-value", pw)

	// The binding still holds through the conversion.
	_, err = tr.OpenToken("other", "ACCESS_TOKEN", SealedFromText(text))
	require.ErrorIs(t, err, ErrSealedSecretInvalid)

	sealed, err := tr.SealToken("acme", "ACCESS_TOKEN", token)
	require.NoError(t, err)
	require.Len(t, text, 110, "the text form of a 32-character token")
	require.Len(t, sealed, 77, "the binary form of a 32-character token: 1 + 4 + 24 + 32 + 16")
	require.Len(t, SealedFromText(text), 77, "the conversion lands on the binary form's size")
}

// What is not the text form is passed through as its own bytes, which Open refuses: a
// plaintext from before sealing existed is a failed authentication, never an absent one.
func TestSealedFromTextPassesAnythingElseThroughToBeRefused(t *testing.T) {
	tr := testTransit(t, 1)
	good := legacyTextSeal(t, tr.tokenAEAD, tr, "tok-0123456789", tokenAAD("acme", "ACCESS_TOKEN"))
	parts := strings.Split(good, "$")
	for name, text := range map[string]string{
		"plaintext":  "tok-0123456789",
		"empty":      "",
		"dcs1":       strings.Replace(good, "dcs2", "dcs1", 1),
		"bad hex id": "dcs2$zzzzzzzz$" + parts[2],
		"short id":   "dcs2$abcd$" + parts[2],
		"bad base64": parts[0] + "$" + parts[1] + "$!!!",
		"four parts": good + "$x",
	} {
		converted := SealedFromText(text)
		require.Equal(t, []byte(text), converted, name)
		_, err := tr.OpenToken("acme", "ACCESS_TOKEN", converted)
		require.ErrorIs(t, err, ErrSealedSecretInvalid, name)
	}
	// Well formed, but under another key: converted, and refused by the key id.
	other := legacyTextSeal(t, tr.tokenAEAD, testTransit(t, 2), "tok-0123456789", tokenAAD("acme", "ACCESS_TOKEN"))
	_, err := tr.OpenToken("acme", "ACCESS_TOKEN", SealedFromText(other))
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "another key's id")
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
	aead, err := chacha20poly1305.NewX(dk.key[:])
	require.NoError(t, err)
	wrong := &SecretTransit{aead: aead, tokenAEAD: aead, id: tr.id}
	sealed, err := tr.Seal("acme", "c", "a-long-enough-secret")
	require.NoError(t, err)
	_, err = wrong.Open("acme", "c", sealed)
	require.ErrorIs(t, err, ErrSealedSecretInvalid)
	sealedWrong, err := wrong.Seal("acme", "c", "a-long-enough-secret")
	require.NoError(t, err)
	_, err = tr.Open("acme", "c", sealedWrong)
	require.ErrorIs(t, err, ErrSealedSecretInvalid)
}

// The nonce is 192 bits, and a password and a token are sealed under separate subkeys: a
// value sealed for one purpose does not open for the other even when the bound data is
// supplied identically, and a dcs1 value from before the cipher change is refused.
func TestSecretTransitNonceSizeAndSeparateSubkeys(t *testing.T) {
	tr := testTransit(t, 1)
	require.Equal(t, 24, tr.aead.NonceSize())
	require.Equal(t, 24, tr.tokenAEAD.NonceSize())

	sealed, err := tr.Seal("acme", "c", "a-long-enough-secret")
	require.NoError(t, err)
	require.Equal(t, sealedMagic, sealed[0])
	require.Equal(t, tr.id[:], sealed[1:sealedHeaderN])
	require.Len(t, sealed, sealedHeaderN+24+len("a-long-enough-secret")+16)

	// Same bound data on both sides, so only the subkey differs.
	cross := &SecretTransit{aead: tr.tokenAEAD, tokenAEAD: tr.aead, id: tr.id}
	_, err = cross.Open("acme", "c", sealed)
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "a password-sealed value opened by the token subkey")
	token, err := tr.SealToken("acme", "ACCESS_TOKEN", "tok-0123456789")
	require.NoError(t, err)
	_, err = cross.OpenToken("acme", "ACCESS_TOKEN", token)
	require.ErrorIs(t, err, ErrSealedSecretInvalid, "a token-sealed value opened by the password subkey")
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
	require.False(t, bytes.Contains(sealed, []byte("tok-0123456789")))
	got, err := tr.OpenToken("acme", "ACCESS_TOKEN", sealed)
	require.NoError(t, err)
	require.Equal(t, "tok-0123456789", got)

	for name, open := range map[string]func() (string, error){
		"another tenant": func() (string, error) { return tr.OpenToken("other", "ACCESS_TOKEN", sealed) },
		"another type":   func() (string, error) { return tr.OpenToken("acme", "MQTT_BASIC", sealed) },
		"as a password":  func() (string, error) { return tr.Open("acme", "tok-0123456789", sealed) },
		"another key":    func() (string, error) { return testTransit(t, 2).OpenToken("acme", "ACCESS_TOKEN", sealed) },
		"plaintext":      func() (string, error) { return tr.OpenToken("acme", "ACCESS_TOKEN", []byte("tok-0123456789")) },
		"empty":          func() (string, error) { return tr.OpenToken("acme", "ACCESS_TOKEN", nil) },
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
