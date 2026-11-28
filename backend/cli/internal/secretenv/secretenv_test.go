// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secretenv

import (
	"os"
	"runtime"
	"testing"
)

func TestCaptureMovesThePassphraseIntoMemory(t *testing.T) {
	t.Cleanup(Forget)
	t.Setenv(EscrowPassphrase, "s3cret")
	Capture()
	if _, ok := os.LookupEnv(EscrowPassphrase); ok {
		t.Fatal("Capture left the passphrase in the environment")
	}
	if !EscrowPassphraseGiven() {
		t.Fatal("the captured passphrase is not reported as given")
	}
	for i := 0; i < 2; i++ {
		if v, ok := LookupEscrowPassphrase(); !ok || v != "s3cret" {
			t.Fatalf("lookup %d: %q, %v", i, v, ok)
		}
	}
}

func TestAnEmptyPassphraseIsStillGiven(t *testing.T) {
	t.Cleanup(Forget)
	t.Setenv(EscrowPassphrase, "")
	Capture()
	if v, ok := LookupEscrowPassphrase(); !ok || v != "" {
		t.Fatalf("an empty passphrase was read as %q, %v; it is the caller's to refuse", v, ok)
	}
}

func TestNothingCapturedIsNothingGiven(t *testing.T) {
	t.Cleanup(Forget)
	t.Setenv(EscrowPassphrase, "")
	os.Unsetenv(EscrowPassphrase)
	Capture()
	if EscrowPassphraseGiven() {
		t.Fatal("a passphrase nobody gave is reported as given")
	}
	if _, ok := LookupEscrowPassphrase(); ok {
		t.Fatal("a passphrase nobody gave was found")
	}
}

func TestScrubDropsOnlyTheSecret(t *testing.T) {
	in := []string{"A=1", EscrowPassphrase + "=x", "DCCTL_ESCROW_PASSPHRASE_FILE=y", "B=" + EscrowPassphrase + "=z"}
	if runtime.GOOS == "windows" {
		in = append(in, "dcctl_escrow_passphrase=w")
	}
	got := Scrub(in)
	want := []string{"A=1", "DCCTL_ESCROW_PASSPHRASE_FILE=y", "B=" + EscrowPassphrase + "=z"}
	if len(got) != len(want) {
		t.Fatalf("Scrub = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Scrub = %q, want %q", got, want)
		}
	}
}
