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
	"strings"
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

// The device accepts the signed golden fixtures. That they are genuinely signed (verify under the
// fixture's own key over the raw digest) is pinned where the fixtures live, in the contract
// package's TestSignedFixturesVerify.
func TestTheDeviceAcceptsEverySignedFixture(t *testing.T) {
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

func TestSignatureStillCheckedWhenUnsignedIsAllowed(t *testing.T) {
	for _, alg := range []string{ota.AlgorithmECDSAP256SHA256, ota.AlgorithmEd25519} {
		t.Run(alg+" corrupted", func(t *testing.T) {
			s := newSigner(t, alg)
			r := signedRig(t, s, s)
			r.cfg.RequireSignature = updater.RequireSignatureOption(false)
			r.up = r.open()
			r.up.SetFaults(updater.Faults{CorruptSignature: true})
			requireFailsWith(t, r, "SIGNATURE_INVALID")
		})
		t.Run(alg+" foreign key", func(t *testing.T) {
			s, other := newSigner(t, alg), newSigner(t, alg)
			r := signedRig(t, s, other)
			r.cfg.RequireSignature = updater.RequireSignatureOption(false)
			r.up = r.open()
			requireFailsWith(t, r, "UNKNOWN_SIGNER")
		})
	}
}

func TestUnsupportedAlgorithmAndTrustAreRefused(t *testing.T) {
	s := newSigner(t, ota.AlgorithmEd25519)
	r := signedRig(t, s, s)
	r.asg.Algorithm = "RSA_PKCS1"
	requireFailsWith(t, r, "SIGNATURE_INVALID")

	r = signedRig(t, s, s)
	r.asg.Trust = "FOO"
	r.assign()
	r.runToEnd()
	a := r.platform.Attempt()
	assert.Equal(t, ota.StateFailed, a.State)
	require.NotNil(t, a.Failure)
	assert.Equal(t, "SIGNATURE_INVALID", a.Failure.Code)
	assert.NotContains(t, stages(r.link), "VERIFIED")
}

func TestHandleDesiredRefusesTrustThatDisagreesWithItsSignatureFields(t *testing.T) {
	r := newClientRig(t)
	d := "sha256:" + strings.Repeat("a", 64)
	body := func(trust, extra string) []byte {
		return []byte(`{"v":1,"kind":"update.assignment","attemptId":"att-9","assignmentId":"asg-9","component":"firmware",` +
			`"trust":"` + trust + `","artifact":{"version":"2","digest":"` + d + `","size":1,"requiresReboot":false}` + extra + `}`)
	}
	key := `"keyId":"` + strings.Repeat("a", 64) + `"`
	sig := `"signature":"AAAA"`
	alg := `"algorithm":"ED25519"`
	for name, b := range map[string][]byte{
		"UNVERIFIED with all three": body("UNVERIFIED", ","+sig+","+key+","+alg),
		"UNVERIFIED with keyId":     body("UNVERIFIED", ","+key),
		"UNVERIFIED with signature": body("UNVERIFIED", ","+sig),
		"UNVERIFIED with algorithm": body("UNVERIFIED", ","+alg),
		"SIGNED with nothing":       body("SIGNED", ""),
		"SIGNED without signature":  body("SIGNED", ","+key+","+alg),
		"SIGNED without keyId":      body("SIGNED", ","+sig+","+alg),
		"SIGNED without algorithm":  body("SIGNED", ","+sig+","+key),
		"unknown trust":             body("FOO", ""),
	} {
		assert.Error(t, r.client.HandleDesired(ctx, b), name)
		assert.Empty(t, r.client.Updater().Snapshot().AttemptID, name+": nothing was assigned")
	}
	require.NoError(t, r.client.HandleDesired(ctx, body("SIGNED", ","+sig+","+key+","+alg)))
}

func TestReassigningWithADifferentSignatureIsRefused(t *testing.T) {
	s := newSigner(t, ota.AlgorithmEd25519)
	r := signedRig(t, s, s)
	r.assign()
	require.NoError(t, r.up.Assign(r.asg), "the identical assignment is a no-op")
	for name, mut := range map[string]func(*updater.Assignment){
		"signature": func(a *updater.Assignment) { a.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64)) },
		"keyId":     func(a *updater.Assignment) { a.KeyID = strings.Repeat("b", 64) },
		"algorithm": func(a *updater.Assignment) { a.Algorithm = ota.AlgorithmECDSAP256SHA256 },
		"trust": func(a *updater.Assignment) {
			a.Trust, a.Signature, a.KeyID, a.Algorithm = ota.TrustUnverified, "", "", ""
		},
	} {
		c := r.asg
		mut(&c)
		assert.Error(t, r.up.Assign(c), name)
	}
}
