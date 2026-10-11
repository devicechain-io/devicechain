// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
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

func TestSecretTransitKeyIsSeparateFromTheDigestKey(t *testing.T) {
	root := make([]byte, 32)
	tr, err := DeriveSecretTransit(root)
	require.NoError(t, err)
	dk, err := DeriveDeviceSecretKey(root)
	require.NoError(t, err)
	require.NotEqual(t, tr.id, dk.KeyId())
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
