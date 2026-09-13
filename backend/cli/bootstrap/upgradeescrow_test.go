// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
)

func aRootKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generating a root key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// An upgrade with an escrow artifact to look at, and a passphrase source it may or
// may not have.
func anUpgradeWithEscrowAt(path, passphraseFile string) UpgradeOptions {
	return UpgradeOptions{EscrowFile: path, EscrowPassphraseFile: passphraseFile}
}

func passphraseFile(t *testing.T, dir, pass string) string {
	t.Helper()
	p := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(p, []byte(pass+"\n"), 0o600); err != nil {
		t.Fatalf("writing the passphrase file: %v", err)
	}
	return p
}

// 🔴 THE CAPABILITY THAT WOULD OTHERWISE HAVE BEEN LOST. An instance built with
// --no-escrow used to gain a second copy of its root key by being bootstrapped again.
// Bootstrap refuses to run against a live instance now, so if this did not work there
// would be no supported way at all — and the key it protects is the one whose loss
// makes every stored secret permanently unreadable, even from a database backup.
func TestAnInstanceWithNoEscrowGainsOneOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rootkey.escrow")
	key := aRootKey(t)
	st := aBootstrapOf(testInstance)

	if got := reconcileUpgradeEscrow(st, key, anUpgradeWithEscrowAt(path, passphraseFile(t, dir, "a-passphrase"))); got != escrowWritten {
		t.Fatalf("the upgrade reported %q rather than writing the escrow this instance lacks", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no escrow was written, so this instance's root key still has one copy: %v", err)
	}
	// ...and it protects the key the instance is actually running on, which is the
	// only property that makes the artifact worth having.
	art, err := escrow.Decode(raw)
	if err != nil {
		t.Fatalf("what was written is not a readable escrow artifact: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(key)
	if !art.Matches(decoded) {
		t.Error("the artifact does not protect the key this instance is running on, so a " +
			"restore from it would recover a cluster that cannot read its own secrets")
	}
}

// Without a passphrase there is nothing to protect an artifact with, so none is
// written — and the upgrade still finishes. The thing being asked for is optional;
// the version change in front of it is not.
func TestAnUpgradeWithNoPassphraseWritesNoEscrowAndStillCompletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rootkey.escrow")
	t.Setenv(EscrowPassphraseEnv, "")
	os.Unsetenv(EscrowPassphraseEnv)

	if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), aRootKey(t), anUpgradeWithEscrowAt(path, "")); got != escrowAbsent {
		t.Errorf("the upgrade reported %q for an instance with no escrow and no passphrase", got)
	}

	if _, err := os.Stat(path); err == nil {
		t.Error("an escrow was written with no passphrase to protect it")
	}
}

// 🔴 AN ARTIFACT THAT PROTECTS A DIFFERENT KEY IS THE WORST STATE THERE IS, because
// it looks exactly like a good one until the day it is used. It most often belongs to
// an instance of the same name that was destroyed and rebuilt. The upgrade must SAY
// so — and must not quietly overwrite it, because it may be the only copy of a key
// some other instance still needs.
func TestAnEscrowForADifferentKeyIsNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rootkey.escrow")
	pass := passphraseFile(t, dir, "a-passphrase")

	// An artifact protecting some earlier instance's key.
	somebodyElsesKey := aRootKey(t)
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "a-passphrase"},
		somebodyElsesKey, testInstance, time.Now().UTC()); err != nil {
		t.Fatalf("writing the artifact that is already there: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	// 🔴 THE OUTCOME, NOT ONLY THE FILE. WriteEscrow independently refuses to
	// overwrite an artifact that is already there, so asserting on the file alone
	// stays true even if this stopped telling a mismatch apart from an absence — and
	// the operator would then be told their key has no second copy while an artifact
	// for a DIFFERENT key sat in its place, unremarked.
	if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), aRootKey(t), anUpgradeWithEscrowAt(path, pass)); got != escrowProtectsADifferentKey {
		t.Errorf("the upgrade reported %q about an artifact protecting a different key", got)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the artifact is gone: %v", err)
	}
	if string(after) != string(before) {
		t.Error("an escrow artifact protecting a different key was overwritten, which may have " +
			"been the only copy of a key another instance still needs")
	}
}

// A matching artifact is left exactly as it is. Rewriting it on every upgrade would
// churn a file operators are told to store somewhere safe, and would change its
// modification time for no reason anybody could explain.
func TestAMatchingEscrowIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rootkey.escrow")
	key := aRootKey(t)
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "a-passphrase"},
		key, testInstance, time.Now().UTC()); err != nil {
		t.Fatalf("writing the artifact: %v", err)
	}
	before, _ := os.ReadFile(path)

	if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), key,
		anUpgradeWithEscrowAt(path, passphraseFile(t, dir, "a-passphrase"))); got != escrowVerified {
		t.Errorf("a matching escrow artifact was reported as %q", got)
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Error("a matching escrow artifact was rewritten")
	}
}

// An instance that uses no secret store has no root key, and reporting "no escrow"
// about it would be naming a gap that does not exist.
func TestAnInstanceWithNoRootKeyIsNotToldItHasNoEscrow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rootkey.escrow")

	if got := reconcileUpgradeEscrow(aBootstrapOf(testInstance), "",
		anUpgradeWithEscrowAt(path, passphraseFile(t, dir, "a-passphrase"))); got != escrowNotApplicable {
		t.Errorf("an instance with no root key was reported as %q", got)
	}

	if _, err := os.Stat(path); err == nil {
		t.Error("an escrow was written for an instance that has no root key to protect")
	}
}
