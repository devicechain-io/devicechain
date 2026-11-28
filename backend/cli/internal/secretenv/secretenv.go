// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package secretenv keeps the secrets dcctl accepts through its environment out of
// the environment of everything dcctl starts.
//
// dcctl execs docker, kind, ko, OpenTofu and its providers, helm plugins and kubeconfig
// exec plugins, and each inherits the process environment; OpenTofu writes its whole
// environment to its log under TF_LOG=trace. A passphrase that is still in dcctl's
// environment when the first of them starts is a passphrase every one of them holds.
//
// 🔴 SO IT IS TAKEN OUT ONCE, AT PROCESS START, BEFORE ANY COMMAND RUNS (Capture), and held
// in memory for the code that needs it. Unsetting it where it is read is not enough:
// that is after the cluster, the registry and the build have already been started, and
// it never happens at all on a path that does not read it (a passphrase file that wins,
// --no-escrow, a command that has no escrow). Scrub is the second barrier, for the places
// that build a child's environment by hand.
package secretenv

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// EscrowPassphrase supplies the root-key escrow passphrase to an automated run that has
// no terminal to prompt at.
const EscrowPassphrase = "DCCTL_ESCROW_PASSPHRASE"

// names are the variables that never reach a child.
var names = []string{EscrowPassphrase}

var (
	mu   sync.Mutex
	held = map[string]string{}
)

// Capture moves every secret variable out of the process environment into memory. The
// process's entry point calls it once, before any command runs.
func Capture() {
	mu.Lock()
	defer mu.Unlock()
	for _, name := range names {
		if v, ok := os.LookupEnv(name); ok {
			held[name] = v
			_ = os.Unsetenv(name)
		}
	}
}

// LookupEscrowPassphrase returns the escrow passphrase the process was given, and whether
// it was given at all (an empty value is given, and is the caller's to refuse). A value
// still in the environment — set after Capture, or with no Capture, as a test does — is
// returned and removed from it; otherwise the one Capture took.
func LookupEscrowPassphrase() (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	if v, ok := os.LookupEnv(EscrowPassphrase); ok {
		_ = os.Unsetenv(EscrowPassphrase)
		return v, true
	}
	v, ok := held[EscrowPassphrase]
	return v, ok
}

// EscrowPassphraseGiven reports whether LookupEscrowPassphrase would find one, without
// taking it.
func EscrowPassphraseGiven() bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := os.LookupEnv(EscrowPassphrase); ok {
		return true
	}
	_, ok := held[EscrowPassphrase]
	return ok
}

// Scrub returns env without any secret variable. Names are matched as the platform
// matches them: case-insensitively on Windows, where DCCTL_Escrow_Passphrase is the same
// variable.
func Scrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !isSecret(kv) {
			out = append(out, kv)
		}
	}
	return out
}

func isSecret(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	for _, n := range names {
		if name == n || (runtime.GOOS == "windows" && strings.EqualFold(name, n)) {
			return true
		}
	}
	return false
}

// Forget drops what Capture took. A test that captures calls it when it ends, so the
// value does not reach the tests after it.
func Forget() {
	mu.Lock()
	defer mu.Unlock()
	held = map[string]string{}
}
