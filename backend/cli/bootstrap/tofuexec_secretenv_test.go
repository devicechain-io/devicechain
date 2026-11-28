// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/secretenv"
)

// OpenTofu and its providers are started with an environment built here, and tofu writes
// all of it to its log under TF_LOG=trace: the escrow passphrase is never part of it.
func TestObservedApplyEnvCarriesNoEscrowPassphrase(t *testing.T) {
	t.Setenv(EscrowPassphraseEnv, "s3cret")
	for _, kv := range observedApplyEnv() {
		if strings.HasPrefix(kv, EscrowPassphraseEnv+"=") {
			t.Fatalf("tofu's environment carries %s", EscrowPassphraseEnv)
		}
	}
}

// The passphrase dcctl's entry point took out of the environment is the one the escrow
// plan uses: moving it into memory must not lose it.
func TestTheEscrowPlanReadsThePassphraseTakenAtProcessStart(t *testing.T) {
	t.Setenv(EscrowPassphraseEnv, "taken-at-start")
	secretenv.Capture()
	t.Cleanup(secretenv.Forget)
	if _, ok := os.LookupEnv(EscrowPassphraseEnv); ok {
		t.Fatal("the passphrase is still in the environment")
	}
	got, err := resolveEscrowPassphrase(EscrowFlags{atTerminal: func() bool { return false }}, escrowQuestion{})
	if err != nil || got != "taken-at-start" {
		t.Fatalf("resolved %q, %v; want the passphrase taken at process start", got, err)
	}
}
