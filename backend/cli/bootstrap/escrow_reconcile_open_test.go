// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	escrowBeginLine = "-----BEGIN DEVICECHAIN ROOT KEY ESCROW-----"
	escrowEndLine   = "-----END DEVICECHAIN ROOT KEY ESCROW-----"
)

// damageEscrowCiphertext flips one byte of the artifact's ciphertext and leaves its
// cleartext header — instance, fingerprint, KDF — exactly as it was, so the file still
// decodes and still claims the key it claimed.
func damageEscrowCiphertext(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	start := strings.Index(s, escrowBeginLine)
	end := strings.Index(s, escrowEndLine)
	if start < 0 || end < 0 {
		t.Fatalf("no escrow block in %s", path)
	}
	var b64 strings.Builder
	for _, line := range strings.Split(s[start+len(escrowBeginLine):end], "\n") {
		b64.WriteString(strings.TrimSpace(line))
	}
	body, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Header     json.RawMessage `json:"hdr"`
		Ciphertext []byte          `json:"ct"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Ciphertext[len(doc.Ciphertext)-1] ^= 0x01
	body, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out := s[:start] + escrowBeginLine + "\n" + base64.StdEncoding.EncodeToString(body) + "\n" + s[end:]
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 🔴 THE HEADER'S FINGERPRINT IS A CLAIM, NOT A CHECK. It is cleartext, outside what the
// passphrase protects, so a re-run that compared only it called a damaged artifact, or one
// it could not open with the passphrase in hand, "verified" — the one word an operator
// reads as "my recovery path works".
func TestReconcileEscrowOpensBeforeItSaysVerified(t *testing.T) {
	t.Parallel()
	written := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "prod.escrow")
		if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("a damaged ciphertext under a matching header is refused", func(t *testing.T) {
		path := written(t)
		damageEscrowCiphertext(t, path)
		outcome, err := ReconcileEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC())
		if err == nil {
			t.Fatalf("a damaged artifact was reported %q", outcome)
		}
		if !strings.Contains(err.Error(), "does not open") {
			t.Errorf("the refusal does not say the artifact does not open: %v", err)
		}
	})

	t.Run("an artifact that does not open with this run's passphrase is refused", func(t *testing.T) {
		path := written(t)
		outcome, err := ReconcileEscrow(EscrowPlan{Path: path, Passphrase: "not-pw"}, testRootKey, "prod", time.Now().UTC())
		if err == nil {
			t.Fatalf("an artifact the passphrase does not open was reported %q", outcome)
		}
	})

	t.Run("with no passphrase it is present, not verified", func(t *testing.T) {
		path := written(t)
		outcome, err := ReconcileEscrow(EscrowPlan{Path: path}, testRootKey, "prod", time.Now().UTC())
		if err != nil {
			t.Fatalf("ReconcileEscrow: %v", err)
		}
		if outcome != string(escrowPresentNotOpened) {
			t.Errorf("outcome = %q, want %q", outcome, escrowPresentNotOpened)
		}
	})

	t.Run("the right passphrase verifies", func(t *testing.T) {
		path := written(t)
		outcome, err := ReconcileEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC())
		if err != nil || outcome != string(escrowVerified) {
			t.Fatalf("outcome = %q, err = %v; want verified", outcome, err)
		}
	})
}

// The upgrade path says the same thing for the same evidence: it warns rather than
// fails, but never calls an unopened or unopenable artifact verified.
func TestAnUpgradeOpensTheEscrowBeforeItSaysVerified(t *testing.T) {
	fakeHome(t)
	t.Setenv(EscrowPassphraseEnv, "")
	os.Unsetenv(EscrowPassphraseEnv)
	dir := t.TempDir()
	key := aRootKey(t)

	t.Run("damaged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rootkey.escrow")
		escrowAt(t, path, key)
		damageEscrowCiphertext(t, path)
		if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), key,
			anUpgradeWithEscrowAt(path, passphraseFile(t, dir, "a-passphrase"))); got != escrowDoesNotOpen {
			t.Errorf("a damaged artifact was reported %q", got)
		}
	})
	t.Run("wrong passphrase", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rootkey.escrow")
		escrowAt(t, path, key)
		if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), key,
			anUpgradeWithEscrowAt(path, passphraseFile(t, t.TempDir(), "another"))); got != escrowDoesNotOpen {
			t.Errorf("an artifact the passphrase does not open was reported %q", got)
		}
	})
	t.Run("no passphrase", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rootkey.escrow")
		escrowAt(t, path, key)
		if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), key, anUpgradeWithEscrowAt(path, "")); got != escrowPresentNotOpened {
			t.Errorf("an artifact nobody opened was reported %q", got)
		}
	})
}
