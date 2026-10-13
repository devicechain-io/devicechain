// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signer is a test key pair: pem is the public half, id the contract's keyId.
type signer struct {
	alg  string
	pem  string
	id   string
	sign func(digest []byte) []byte
}

func newSigner(t *testing.T, alg string) signer {
	t.Helper()
	var pub any
	var sign func([]byte) []byte
	switch alg {
	case ota.AlgorithmECDSAP256SHA256:
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		pub = &k.PublicKey
		sign = func(d []byte) []byte {
			s, err := ecdsa.SignASN1(rand.Reader, k, d)
			require.NoError(t, err)
			return s
		}
	case ota.AlgorithmEd25519:
		p, k, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		pub = p
		sign = func(d []byte) []byte { return ed25519.Sign(k, d) }
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	sum := sha256.Sum256(spki)
	return signer{alg: alg, id: hex.EncodeToString(sum[:]), sign: sign,
		pem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki}))}
}

// signedRig is a rig whose assignment is SIGNED by s over the image's raw digest, on a device
// that trusts trusted.
func signedRig(t *testing.T, s signer, trusted ...signer) *rig {
	t.Helper()
	r := newRig(t, true)
	raw := sha256.Sum256(r.src.data)
	r.asg.Trust = ota.TrustSigned
	r.asg.Signature = base64.StdEncoding.EncodeToString(s.sign(raw[:]))
	r.asg.KeyID = s.id
	r.asg.Algorithm = s.alg
	r.cfg.RequireSignature = updater.RequireSignatureOption(true)
	r.cfg.TrustedKeys = nil
	for _, k := range trusted {
		r.cfg.TrustedKeys = append(r.cfg.TrustedKeys, k.pem)
	}
	r.up = r.open()
	return r
}

func TestSignedUpdateInstallsForEachAlgorithm(t *testing.T) {
	for _, alg := range []string{ota.AlgorithmECDSAP256SHA256, ota.AlgorithmEd25519} {
		t.Run(alg, func(t *testing.T) {
			s := newSigner(t, alg)
			r := signedRig(t, s, s)
			r.assign()
			r.runToEnd()
			assert.Equal(t, ota.StateUpdated, r.state())
			assert.Equal(t, "2.0.0", r.up.Snapshot().RunningVersion)
		})
	}
}

func requireFailsWith(t *testing.T, r *rig, code string) {
	t.Helper()
	r.assign()
	r.runToEnd()
	a := r.platform.Attempt()
	assert.Equal(t, ota.StateFailed, a.State)
	require.NotNil(t, a.Failure)
	assert.Equal(t, code, a.Failure.Code)
	assert.NotContains(t, stages(r.link), "VERIFIED", "an update that fails authenticity is never VERIFIED")
	assert.NotContains(t, stages(r.link), "INSTALLING")
	requireEveryFrameDecodes(t, r.link)
}

func TestSignatureNegativeControls(t *testing.T) {
	for _, alg := range []string{ota.AlgorithmECDSAP256SHA256, ota.AlgorithmEd25519} {
		t.Run(alg+" corrupted signature", func(t *testing.T) {
			s := newSigner(t, alg)
			r := signedRig(t, s, s)
			r.up.SetFaults(updater.Faults{CorruptSignature: true})
			requireFailsWith(t, r, "SIGNATURE_INVALID")
		})
		t.Run(alg+" signature over other bytes", func(t *testing.T) {
			s := newSigner(t, alg)
			r := signedRig(t, s, s)
			other := sha256.Sum256([]byte("not the image"))
			r.asg.Signature = base64.StdEncoding.EncodeToString(s.sign(other[:]))
			requireFailsWith(t, r, "SIGNATURE_INVALID")
		})
		t.Run(alg+" foreign key", func(t *testing.T) {
			s, other := newSigner(t, alg), newSigner(t, alg)
			r := signedRig(t, s, other) // the device trusts someone else
			requireFailsWith(t, r, "UNKNOWN_SIGNER")
		})
	}
	t.Run("algorithm does not match the key", func(t *testing.T) {
		s := newSigner(t, ota.AlgorithmEd25519)
		r := signedRig(t, s, s)
		r.asg.Algorithm = ota.AlgorithmECDSAP256SHA256
		requireFailsWith(t, r, "SIGNATURE_INVALID")
	})
	t.Run("a tampered download is a digest failure, not a signature one", func(t *testing.T) {
		s := newSigner(t, ota.AlgorithmEd25519)
		r := signedRig(t, s, s)
		r.src.data[3*chunk+1] ^= 0xff
		requireFailsWith(t, r, "VERIFICATION_FAILED")
	})
	t.Run("UNVERIFIED while a signature is required", func(t *testing.T) {
		r := newRig(t, true)
		r.cfg.RequireSignature = nil // the default
		r.up = r.open()
		requireFailsWith(t, r, "UNKNOWN_SIGNER")
	})
	t.Run("UNVERIFIED is accepted only when the device says so", func(t *testing.T) {
		r := newRig(t, true) // rig sets RequireSignature false
		r.assign()
		r.runToEnd()
		assert.Equal(t, ota.StateUpdated, r.state())
	})
}

func TestTrustedKeysAreValidated(t *testing.T) {
	r := newRig(t, true)
	bad := map[string]string{
		"not pem":     "hello",
		"private key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("SECRET-DO-NOT-ECHO")})),
		"junk spki":   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("junk")})),
	}
	for name, p := range bad {
		r.cfg.TrustedKeys = []string{p}
		_, err := updater.Open(r.cfg)
		require.ErrorIs(t, err, updater.ErrBadTrustedKey, name)
		assert.NotContains(t, err.Error(), "SECRET-DO-NOT-ECHO", name)
	}
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	spki, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	require.NoError(t, err)
	r.cfg.TrustedKeys = []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki}))}
	_, err = updater.Open(r.cfg)
	require.ErrorIs(t, err, updater.ErrBadTrustedKey, "only P-256 is accepted")
}

// The signed golden fixtures are genuinely signed: the device accepts them, and each verifies
// under the fixture's own public key over the fixture digest's raw bytes. A fixture that merely
// parsed would let the contract and the device agree on a signature nobody can verify.
func TestSignedFixturesAreAcceptedAndVerify(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "core", "ota", "testdata", "update")
	for name, keyFile := range map[string]string{
		"update-assignment-signed-ecdsa":   "signer-ecdsa-p256.pub.pem",
		"update-assignment-signed-ed25519": "signer-ed25519.pub.pem",
	} {
		raw, err := os.ReadFile(filepath.Join(dir, "desired", name+".json"))
		require.NoError(t, err)
		pemBytes, err := os.ReadFile(filepath.Join(dir, "keys", keyFile))
		require.NoError(t, err)

		c := newClientRig(t)
		c.cfg.TrustedKeys = []string{string(pemBytes)}
		c.client = c.open()
		require.NoError(t, c.client.HandleDesired(ctx, raw), name)
		require.Equal(t, "att-0001", c.client.Updater().Snapshot().AttemptID, name)
	}
}
