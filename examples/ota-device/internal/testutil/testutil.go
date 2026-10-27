// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package testutil builds the real binaries and the signing keys the tests need. It is imported
// only by tests.
package testutil

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// ModuleRoot is the directory holding go.mod.
func ModuleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

var (
	cacheMu  sync.Mutex
	cacheDir string
	built    = map[string]string{}
)

// Cleanup removes the build cache. Call it from TestMain.
func Cleanup() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheDir != "" {
		_ = os.RemoveAll(cacheDir)
		cacheDir = ""
	}
}

// Build compiles the package (a path relative to the module root) with the given -ldflags and
// returns the path of the binary. Identical requests are built once per test process.
func Build(t testing.TB, pkg, ldflags string) string {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	key := pkg + "|" + ldflags
	if p, ok := built[key]; ok {
		return p
	}
	if cacheDir == "" {
		d, err := os.MkdirTemp("", "ota-device-test-build-")
		if err != nil {
			t.Fatal(err)
		}
		cacheDir = d
	}
	sum := sha256.Sum256([]byte(key))
	out := filepath.Join(cacheDir, hex.EncodeToString(sum[:6]))
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "-ldflags", ldflags, pkg)
	cmd.Dir = ModuleRoot()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
	built[key] = out
	return out
}

// Fake builds the fake firmware image with the given identity and behaviour.
func Fake(t testing.TB, version, product, mode string) string {
	t.Helper()
	ld := "-X main.version=" + version + " -X main.product=" + product + " -X main.mode=" + mode
	return Build(t, "./internal/testutil/fakeimage", ld)
}

// Signer is a signing key in one of the two algorithms the contract allows.
type Signer struct {
	Algorithm string
	KeyID     string
	PEM       []byte
	sign      func(digest []byte) []byte
}

func newSigner(alg string, pub any, sign func([]byte) []byte) *Signer {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(der)
	return &Signer{Algorithm: alg, KeyID: hex.EncodeToString(sum[:]), sign: sign,
		PEM: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})}
}

// NewECDSA returns a P-256 signer (ECDSA_P256_SHA256).
func NewECDSA() *Signer {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return newSigner("ECDSA_P256_SHA256", &k.PublicKey, func(d []byte) []byte {
		s, err := ecdsa.SignASN1(rand.Reader, k, d)
		if err != nil {
			panic(err)
		}
		return s
	})
}

// NewEd25519 returns an Ed25519 signer.
func NewEd25519() *Signer {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return newSigner("ED25519", pub, func(d []byte) []byte { return ed25519.Sign(priv, d) })
}

// Sign signs the raw 32 bytes of a "sha256:<hex>" digest, as the contract requires.
func (s *Signer) Sign(digest string) string {
	raw, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(s.sign(raw))
}
