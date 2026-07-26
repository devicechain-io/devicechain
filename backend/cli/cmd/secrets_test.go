// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
)

// The recovery instructions printed inside every escrow artifact must name a
// command dcctl actually has, with flags it actually accepts.
//
// This is a cross-module claim that neither side can check alone: core writes the
// prose, dcctl owns the command, and nothing links them. The first version of that
// preamble told operators to run `dcctl secrets rehydrate --escrow-file`, which was
// never built — and it took running the binary to notice, because every test on
// both sides was green.
//
// It matters more than an ordinary stale doc. This file is read by someone whose
// cluster is gone, possibly years later, possibly having never seen one before, and
// it is explicitly written to be usable when no other documentation can be reached.
// Instructions that do not run are worse than none: they cost the reader their
// remaining confidence that the file is what it says it is.
func TestTheArtifactNamesARealRecoveryCommand(t *testing.T) {
	key := make([]byte, escrow.RootKeySize)
	art, err := escrow.Wrap(key, "pass", "prod",
		escrow.KDFParams{Alg: escrow.KDFArgon2id, Time: 1, Memory: 8, Threads: 1}, time.Now().UTC())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	raw, err := art.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	line := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, escrow.RecoveryCommand) {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatalf("the artifact never tells the reader how to recover: no line contains %q", escrow.RecoveryCommand)
	}

	fields := strings.Fields(line)
	if fields[0] != rootCmd.Name() {
		t.Fatalf("the recovery line invokes %q, not %q", fields[0], rootCmd.Name())
	}
	cmd, _, err := rootCmd.Find(fields[1:])
	if err != nil {
		t.Fatalf("the recovery line %q does not resolve to a dcctl command: %v", line, err)
	}
	if cmd == rootCmd {
		t.Fatalf("the recovery line %q resolved to the root command, so it names no subcommand", line)
	}

	// Every flag the line tells the operator to type must exist on that command.
	flags := 0
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "--") {
			continue
		}
		flags++
		name := strings.TrimPrefix(f, "--")
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("the recovery line tells the operator to pass --%s, which `%s %s` does not accept",
				name, rootCmd.Name(), cmd.Name())
		}
	}
	if flags == 0 {
		t.Fatalf("the recovery line %q names no flag, so this check verified nothing about how to "+
			"actually supply the artifact", line)
	}
}
