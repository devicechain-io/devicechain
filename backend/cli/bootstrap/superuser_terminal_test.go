// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// pretendStdoutIsATerminal makes the direct (no engine run) report treat its stdout as a
// person's terminal, the one place the seed password may be printed.
func pretendStdoutIsATerminal(t *testing.T) {
	t.Helper()
	orig := stdoutIsTerminal
	t.Cleanup(func() { stdoutIsTerminal = orig })
	stdoutIsTerminal = func(io.Writer) bool { return true }
}

// 🔴 THE SEED PASSWORD IS NEVER PRINTED TO OUTPUT THAT IS NOT A TERMINAL. Text output piped
// or redirected is a CI log or a file, kept and read by people the password was never for:
// there the report names the Secret instead. On a terminal it is shown once, as it was.
func TestTheSeedPasswordIsShownOnlyOnATerminal(t *testing.T) {
	ref := superuserSecretRef("acme")
	for _, terminal := range []bool{false, true} {
		name := map[bool]string{false: "not a terminal", true: "a terminal"}[terminal]
		t.Run("plain presenter, "+name, func(t *testing.T) {
			seed := canaryValue(t, "superuser-seed")
			st := goldenBootstrapState(nil)
			st.Credentials.SuperuserPassword = seed
			e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, Escrow: EscrowFlags{NoEscrow: true},
				Target:   func(context.Context, Console, EscrowPlan) (*State, error) { return st, nil },
				Pipeline: reportOnlyPipeline(t)}
			var stdout, stderr bytes.Buffer
			err := installplain.Presenter{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""),
				StdoutIsTerminal: func() bool { return terminal }}.Run(context.Background(), e,
				installflow.Request{Operation: installflow.OperationBootstrap}, nil)
			if err != nil {
				t.Fatal(err)
			}
			checkSeedReport(t, stdout.String(), seed, ref, terminal)
		})
		t.Run("direct report, "+name, func(t *testing.T) {
			seed := canaryValue(t, "superuser-seed")
			st := goldenBootstrapState(nil)
			st.Credentials.SuperuserPassword = seed
			orig := stdoutIsTerminal
			t.Cleanup(func() { stdoutIsTerminal = orig })
			stdoutIsTerminal = func(io.Writer) bool { return terminal }
			out := captureStdout(t, func() { printSuperuserReport(context.Background(), st) })
			checkSeedReport(t, out, seed, ref, terminal)
		})
	}
}

func checkSeedReport(t *testing.T, out, seed string, ref mintedCredentialRef, terminal bool) {
	t.Helper()
	if n := strings.Count(out, seed); n != map[bool]int{false: 0, true: 1}[terminal] {
		t.Fatalf("terminal=%v: the seed appears %d times:\n%s", terminal, n, out)
	}
	if !strings.Contains(out, "secret "+ref.Name) {
		t.Errorf("the report does not name the Secret the seed is kept in:\n%s", out)
	}
	if !terminal && !strings.Contains(out, "not shown by this run") {
		t.Errorf("the report does not say the password was not shown:\n%s", out)
	}
}
