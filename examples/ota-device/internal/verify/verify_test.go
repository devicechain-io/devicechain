// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/testutil"
	"devicechain.io/examples/ota-device/internal/verify"
	"devicechain.io/examples/ota-device/internal/wire"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func code(t *testing.T, err error) string {
	t.Helper()
	var f *verify.Failure
	require.True(t, errors.As(err, &f), "want a *verify.Failure, got %v", err)
	return f.Code
}

func TestDigestMustMatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "img")
	require.NoError(t, os.WriteFile(p, []byte("firmware"), 0o600))
	require.NoError(t, verify.CheckDigest(p, digestOf([]byte("firmware"))))
	// Negative control: one flipped byte is refused.
	assert.Equal(t, verify.CodeVerificationFailed, code(t, verify.CheckDigest(p, digestOf([]byte("firmwarf")))))
}

func trust(t *testing.T, ss ...*testutil.Signer) *verify.TrustSet {
	t.Helper()
	var pem []byte
	for _, s := range ss {
		pem = append(pem, s.PEM...)
	}
	ts, err := verify.ParseTrust(pem)
	require.NoError(t, err)
	return ts
}

func TestSignatureVerification(t *testing.T) {
	d := digestOf([]byte("image"))
	for _, s := range []*testutil.Signer{testutil.NewECDSA(), testutil.NewEd25519()} {
		t.Run(s.Algorithm, func(t *testing.T) {
			ts := trust(t, s)
			require.NoError(t, ts.VerifySignature(d, s.KeyID, s.Algorithm, s.Sign(d)))

			// signature over other bytes
			assert.Equal(t, wire.CodeSignatureInvalid, code(t, ts.VerifySignature(digestOf([]byte("other")), s.KeyID, s.Algorithm, s.Sign(d))))
			// a signature made by a different key, presented under this key's id
			if s.Algorithm == wire.AlgECDSA {
				other := testutil.NewECDSA()
				assert.Equal(t, wire.CodeSignatureInvalid, code(t, ts.VerifySignature(d, s.KeyID, s.Algorithm, other.Sign(d))))
			}
			// the algorithm must match the key type
			wrong := wire.AlgEd25519
			if s.Algorithm == wire.AlgEd25519 {
				wrong = wire.AlgECDSA
			}
			assert.Equal(t, wire.CodeSignatureInvalid, code(t, ts.VerifySignature(d, s.KeyID, wrong, s.Sign(d))))
			// not base64
			assert.Equal(t, wire.CodeSignatureInvalid, code(t, ts.VerifySignature(d, s.KeyID, s.Algorithm, "***")))
		})
	}
}

// A key the device does not hold is UNKNOWN_SIGNER even though its signature is perfectly good:
// the wire keyId only selects from the local set, it never supplies a key.
func TestUnknownSignerIsRefusedEvenWithAValidSignature(t *testing.T) {
	trusted, stranger := testutil.NewECDSA(), testutil.NewEd25519()
	d := digestOf([]byte("image"))
	err := trust(t, trusted).VerifySignature(d, stranger.KeyID, stranger.Algorithm, stranger.Sign(d))
	assert.Equal(t, wire.CodeUnknownSigner, code(t, err))
}

// The signed fixtures the contract publishes verify with the public keys it publishes, over the
// digest those fixtures carry. This pins what is signed (the raw 32 digest bytes) for both
// algorithms against an independent source.
func TestContractSignedFixturesVerify(t *testing.T) {
	fx := filepath.Join("..", "..", "..", "..", "backend", "core", "ota", "testdata", "update")
	pemE, err := os.ReadFile(filepath.Join(fx, "keys", "signer-ecdsa-p256.pub.pem"))
	require.NoError(t, err)
	pemD, err := os.ReadFile(filepath.Join(fx, "keys", "signer-ed25519.pub.pem"))
	require.NoError(t, err)
	ts, err := verify.ParseTrust(append(pemE, pemD...))
	require.NoError(t, err)
	require.Equal(t, 2, ts.Len())
	for _, n := range []string{"update-assignment-signed-ecdsa.json", "update-assignment-signed-ed25519.json"} {
		b, err := os.ReadFile(filepath.Join(fx, "desired", n))
		require.NoError(t, err)
		m, err := wire.DecodeDesired(b)
		require.NoError(t, err)
		a := m.Assignment
		assert.NoError(t, ts.VerifySignature(a.Artifact.Digest, a.KeyID, a.Algorithm, a.Signature), n)
		// ...and not over a different digest.
		assert.Error(t, ts.VerifySignature(digestOf([]byte("x")), a.KeyID, a.Algorithm, a.Signature), n)
	}
}

func TestTrustSetParsing(t *testing.T) {
	_, err := verify.ParseTrust(nil)
	assert.Error(t, err, "an empty trust set must not look configured")
	_, err = verify.ParseTrust([]byte("not pem"))
	assert.Error(t, err)
	s := testutil.NewECDSA()
	_, err = verify.ParseTrust(append(s.PEM, []byte("garbage")...))
	assert.Error(t, err, "trailing data after the last block")
}

func asg(component, version, trustv string) wire.Assignment {
	return wire.Assignment{Component: component, Trust: trustv, Artifact: wire.Artifact{Version: version}}
}

func TestPolicy(t *testing.T) {
	base := verify.Policy{Component: "firmware", Current: "1.0.1", RequireSigned: true}
	cases := []struct {
		name string
		p    verify.Policy
		a    wire.Assignment
		code string // "" = allowed
	}{
		{"newer", base, asg("firmware", "1.0.2", wire.TrustSigned), ""},
		{"newer minor", base, asg("firmware", "1.1.0", wire.TrustSigned), ""},
		{"prerelease of a newer patch", base, asg("firmware", "1.0.2-crash", wire.TrustSigned), ""},
		{"older", base, asg("firmware", "1.0.0", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"equal is a replay", base, asg("firmware", "1.0.1", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"prerelease of the running version is older", base, asg("firmware", "1.0.1-rc.1", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"build metadata does not make it newer", base, asg("firmware", "1.0.1+b7", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"wrong component", base, asg("bootloader", "9.9.9", wire.TrustSigned), verify.CodeWrongComponent},
		{"not semver fails closed", base, asg("firmware", "latest", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"leading v fails closed", base, asg("firmware", "v2.0.0", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"leading zero fails closed", base, asg("firmware", "1.01.0", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"running version not semver fails closed", verify.Policy{Component: "firmware", Current: "dev", RequireSigned: true}, asg("firmware", "2.0.0", wire.TrustSigned), verify.CodeDowngradeRefused},
		{"unsigned refused", base, asg("firmware", "1.0.2", wire.TrustUnverified), wire.CodeUnknownSigner},
		{"unsigned allowed when not required", verify.Policy{Component: "firmware", Current: "1.0.1"}, asg("firmware", "1.0.2", wire.TrustUnverified), ""},
		{"downgrade allowed by flag", verify.Policy{Component: "firmware", Current: "1.0.1", RequireSigned: true, AllowDowngrade: true}, asg("firmware", "0.1.0", wire.TrustSigned), ""},
		{"flag does not unlock the component", verify.Policy{Component: "firmware", Current: "1.0.1", AllowDowngrade: true}, asg("bootloader", "2.0.0", wire.TrustSigned), verify.CodeWrongComponent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.CheckPolicy(c.a)
			if c.code == "" {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, c.code, code(t, err))
		})
	}
}

func TestSemverPrecedence(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := range order {
		for j := range order {
			got, err := verify.CompareVersions(order[i], order[j])
			require.NoError(t, err)
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			assert.Equal(t, want, got, "%s vs %s", order[i], order[j])
		}
	}
}

// Describe runs a real binary, so this also shows the preflight really executes what it is given
// (the positive control for the order tests elsewhere).
func TestDescribeAndIdentity(t *testing.T) {
	img := testutil.Fake(t, "1.0.1", "ota-device-example", "good")
	id, err := verify.Describe(context.Background(), img, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, verify.Identity{Product: "ota-device-example", Version: "1.0.1"}, id)
	assert.NoError(t, verify.CheckIdentity(id, "ota-device-example", "1.0.1"))
	assert.Equal(t, verify.CodeImageMismatch, code(t, verify.CheckIdentity(id, "ota-device-example", "1.0.2")), "wrong version")
	assert.Equal(t, verify.CodeImageMismatch, code(t, verify.CheckIdentity(id, "other-product", "1.0.1")), "wrong product")
}

func TestDescribeFailsOnAnImageThatCannotRun(t *testing.T) {
	_, err := verify.Describe(context.Background(), filepath.Join(t.TempDir(), "nothing"), time.Second)
	assert.Error(t, err)
}

func TestNilTrustSetRefusesInsteadOfPanicking(t *testing.T) {
	var ts *verify.TrustSet
	s := testutil.NewECDSA()
	d := digestOf([]byte("image"))
	assert.Equal(t, wire.CodeUnknownSigner, code(t, ts.VerifySignature(d, s.KeyID, s.Algorithm, s.Sign(d))))
	assert.Equal(t, 0, ts.Len())
	assert.Empty(t, ts.KeyIDs())
}
