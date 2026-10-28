// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509sim

import (
	"context"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

func TestOpenOrMintAndTopUp(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenOrMint(dir, Options{Prefix: "sim-", Pinned: 2, JIT: 1})
	require.NoError(t, err)
	require.Len(t, st.Entries(KindPinned), 2)
	require.Len(t, st.Entries(KindJIT), 1)
	pinned := st.Entries(KindPinned)
	require.Equal(t, "sim-pin-0001", pinned[0].ExternalId)
	require.Equal(t, core.DeriveDeviceToken("sim-pin-0001", "pv-"), pinned[0].Token)

	// Re-opening keeps the CA and every certificate; asking for more tops up.
	st2, err := OpenOrMint(dir, Options{Prefix: "sim-", Pinned: 3, JIT: 1})
	require.NoError(t, err)
	require.Equal(t, st.CA.Cert.Raw, st2.CA.Cert.Raw)
	require.Len(t, st2.Entries(KindPinned), 3)
	require.Equal(t, pinned[0].Fingerprint, st2.Entries(KindPinned)[0].Fingerprint)

	// Every minted certificate chains to the CA through the platform's verifier.
	for _, e := range st2.Manifest.Devices {
		leaf, err := st2.Leaf(e)
		require.NoError(t, err)
		require.Equal(t, e.ExternalId, leaf.Subject.CommonName)
		_, err = x509trust.VerifyClientChain(st2.CA.Cert, []*x509.Certificate{leaf}, time.Now())
		require.NoError(t, err, e.ExternalId)
	}
}

func TestKeysAreOwnerOnlyAndNotInTheManifest(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenOrMint(dir, Options{Pinned: 1})
	require.NoError(t, err)

	manifest, err := os.ReadFile(filepath.Join(dir, manifestFile))
	require.NoError(t, err)
	require.NotContains(t, string(manifest), "PRIVATE KEY")
	pub, err := os.ReadFile(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	require.NotContains(t, string(pub), "PRIVATE")

	if runtime.GOOS != "windows" {
		for _, f := range []string{caKeyFile, st.Manifest.Devices[0].KeyFile} {
			fi, err := os.Stat(filepath.Join(dir, f))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), f)
		}
	}
}

type call struct {
	query string
	vars  map[string]any
}

type recorder struct{ calls []call }

func (r *recorder) Query(_ context.Context, _ string, q string, v map[string]any, _ any) error {
	r.calls = append(r.calls, call{q, v})
	return nil
}

func TestRegisterCreatesPinnedOnlyAndNeverJIT(t *testing.T) {
	st, err := OpenOrMint(t.TempDir(), Options{Prefix: "r-", Pinned: 1, JIT: 2})
	require.NoError(t, err)
	r := &recorder{}
	require.NoError(t, Register(context.Background(), r, RegisterConfig{Endpoint: "http://dm", DeviceTypeToken: "dt"}, st))

	var creds []map[string]any
	for _, c := range r.calls {
		if c.query == mutationCreateCred {
			creds = append(creds, c.vars["request"].(map[string]any))
		}
		if c.query == mutationCreateDevice {
			require.Equal(t, st.Entries(KindPinned)[0].Token, c.vars["request"].(map[string]any)["token"])
		}
	}
	require.Len(t, creds, 1)
	require.Equal(t, credentialTypeX509, creds[0]["credentialType"])
	require.Equal(t, "", creds[0]["credentialId"], "the server computes the pin and refuses a supplied one")
	require.Equal(t, st.Manifest.AnchorToken, creds[0]["trustAnchorToken"])
	require.Contains(t, creds[0]["certificatePem"], "BEGIN CERTIFICATE")
	require.NotContains(t, creds[0]["certificatePem"], "PRIVATE")

	for _, c := range r.calls {
		for _, j := range st.Entries(KindJIT) {
			for _, v := range c.vars {
				if m, ok := v.(map[string]any); ok {
					require.NotEqual(t, j.Token, m["token"], "a JIT device must never be registered")
					require.NotEqual(t, j.Token, m["deviceToken"])
				}
			}
		}
	}
}

func TestRegisterNeedsEndpointAndType(t *testing.T) {
	st, err := OpenOrMint(t.TempDir(), Options{Pinned: 1})
	require.NoError(t, err)
	require.Error(t, Register(context.Background(), &recorder{}, RegisterConfig{}, st))
}
