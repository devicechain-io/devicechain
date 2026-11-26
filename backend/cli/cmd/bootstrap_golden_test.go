// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

// 🔴 THESE GOLDENS ARE A CONTRACT, NOT A RECORDING, exactly as install's are (see
// install_golden_test.go, whose child harness they share). They characterise what
// `dcctl bootstrap` prints, on which stream and with which exit status, from argv up to
// the first thing that needs a cluster: the refusals settled from the arguments, the
// preflight, the root-key escrow's passphrase sources in their order of precedence, and
// the cluster lookup that ends every run here (no scenario has a cluster). The pipeline
// and its closing report are characterised in-process, in
// bootstrap/bootstrap_golden_test.go.
//
// They were captured BEFORE the bootstrap was run through an engine, so a change to one
// in a PR that set out to change no output is that PR's bug.

const bootstrapGoldenDir = "testdata/bootstrap_golden"

// bootstrapCanaryPassphrase is the escrow-passphrase canary, handed to a run through a
// file or the environment. No golden may hold any part of it.
func bootstrapCanaryPassphrase() string {
	for _, c := range testkit.Canaries() {
		if c.Label == "escrow-passphrase" {
			return c.Value
		}
	}
	panic("no escrow-passphrase canary")
}

var bootstrapScenarios = func() []installScenario {
	pass := bootstrapCanaryPassphrase()
	return []installScenario{
		// Refusals settled from argv, before any host or cluster is asked anything.
		{name: "missing-instance", args: []string{"bootstrap", "local"}},
		{name: "unknown-provider", args: []string{"bootstrap", "gcp", "acme"}},
		{name: "refuse-dev-host", args: []string{"bootstrap", "local", "acme", "--dev", "--host", "iot.example.com"}},
		{name: "refuse-unknown-area", args: []string{"bootstrap", "local", "acme", "--enable-area", "nope"}},
		{name: "refuse-instance-name", args: []string{"bootstrap", "local", "Acme_1", "--skip-preflight", "--no-escrow"}},
		{name: "refuse-no-escrow-with-restore", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--no-escrow", "--restore-root-key", "acme.escrow"}},
		{name: "refuse-no-escrow-with-escrow-file", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--no-escrow", "--escrow-file", "acme.escrow"}},
		{name: "refuse-unpinned-build", args: []string{"bootstrap", "local", "acme", "--skip-preflight", "--no-escrow"},
			unpinned: true},

		// Preflight. No scenario has a kind cluster, so a run the preflight lets through
		// ends at the cluster lookup.
		{name: "preflight-small-host", args: []string{"bootstrap", "local", "acme", "--no-escrow"}, host: hostSmall},
		{name: "preflight-pass", args: []string{"bootstrap", "local", "acme", "--no-escrow"}},

		// The escrow passphrase, source by source: --escrow-passphrase-file, then
		// DCCTL_ESCROW_PASSPHRASE, then a prompt only at a terminal (stdin here is a pipe).
		{name: "escrow-no-source-no-terminal", args: []string{"bootstrap", "local", "acme", "--skip-preflight"},
			stdin: "typed-into-a-pipe\n"},
		{name: "escrow-file-empty", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--escrow-passphrase-file", "pass.txt"}, files: map[string]string{"pass.txt": ""}},
		{name: "escrow-file-only-newlines", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--escrow-passphrase-file", "pass.txt"}, files: map[string]string{"pass.txt": "\r\n\n"}},
		{name: "escrow-file", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--escrow-passphrase-file", "pass.txt"}, files: map[string]string{"pass.txt": pass + "\n"}},
		// The file wins over the environment: an empty variable, which would fail the run
		// if it were consulted, is not.
		{name: "escrow-file-before-env", args: []string{"bootstrap", "local", "acme", "--skip-preflight",
			"--escrow-passphrase-file", "pass.txt"}, files: map[string]string{"pass.txt": pass},
			env: map[string]string{bootstrap.EscrowPassphraseEnv: ""}},
		{name: "escrow-env-empty", args: []string{"bootstrap", "local", "acme", "--skip-preflight"},
			env: map[string]string{bootstrap.EscrowPassphraseEnv: ""}},
		// The environment wins over the prompt, so a piped stdin is never read.
		{name: "escrow-env", args: []string{"bootstrap", "local", "acme", "--skip-preflight"},
			env: map[string]string{bootstrap.EscrowPassphraseEnv: pass}, stdin: "typed-into-a-pipe\n"},
		// A dry run asks for no passphrase at all.
		{name: "escrow-dry-run", args: []string{"bootstrap", "local", "acme", "--skip-preflight", "--dry-run"}},

		// The rest of argv, up to the cluster.
		{name: "dev", args: []string{"bootstrap", "local", "scratch", "--dev", "--skip-preflight"}},
		{name: "dev-escrow-kept", args: []string{"bootstrap", "local", "scratch", "--dev", "--skip-preflight",
			"--no-escrow=false"}, env: map[string]string{bootstrap.EscrowPassphraseEnv: pass}},
		{name: "kube-context-not-found", args: []string{"bootstrap", "local", "acme", "--kube-context", "prod",
			"--skip-preflight", "--no-escrow"}},
	}
}()

// TestBootstrapGoldens runs each bootstrap scenario in a child and compares what it said
// with the committed golden.
func TestBootstrapGoldens(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range installScenarios {
		seen[sc.name] = true
	}
	for _, sc := range bootstrapScenarios {
		if seen[sc.name] && sc.name != "unknown-provider" && sc.name != "kube-context-not-found" &&
			sc.name != "refuse-unpinned-build" {
			t.Fatalf("scenario %q is also an install scenario", sc.name)
		}
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			got := runInstallChild(t, sc)
			testkit.AssertNoCanary(t, "bootstrap golden "+sc.name, got)
			compareInstallGolden(t, filepath.Join(bootstrapGoldenDir, sc.name+".txt"), got)
		})
	}
}

// TestBootstrapGoldensHaveScenarios refuses a golden with no scenario behind it.
func TestBootstrapGoldensHaveScenarios(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range bootstrapScenarios {
		if seen[sc.name] {
			t.Fatalf("duplicate scenario %q", sc.name)
		}
		seen[sc.name] = true
	}
	entries, err := os.ReadDir(bootstrapGoldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".txt"); ok && !seen[n] {
			t.Errorf("golden %s has no scenario", e.Name())
		}
	}
}
