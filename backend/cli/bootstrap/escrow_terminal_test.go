// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// A presenter that owns the terminal (the terminal UI puts the null device on stdin
// while the run is live) says a person is there with atTerminal, in place of looking at
// stdin. It changes who may be asked and nothing about the order: a file and the
// environment still come first, and without a person nobody is asked.
func TestAtTerminalReplacesTheStdinCheckAndNothingElse(t *testing.T) {
	clearEscrowEnv(t)
	noTerminal(t) // stdin is not a terminal, as under the terminal UI
	asked := 0
	ask := func(escrowQuestion) (string, error) { asked++; return "typed-on-screen", nil }
	q := escrowQuestion{purpose: "p", confirm: true}

	// No presenter's word: stdin decides, and nobody is asked.
	if _, err := resolveEscrowPassphrase(EscrowFlags{ask: ask}, q); err == nil || asked != 0 {
		t.Fatalf("with no terminal: err %v, asked %d", err, asked)
	}
	// The presenter says nobody is there: still nobody is asked.
	if _, err := resolveEscrowPassphrase(EscrowFlags{ask: ask, atTerminal: func() bool { return false }}, q); err == nil || asked != 0 {
		t.Fatalf("a presenter with no person: err %v, asked %d", err, asked)
	}
	// The presenter says a person is there: they are asked, through ask.
	got, err := resolveEscrowPassphrase(EscrowFlags{ask: ask, atTerminal: func() bool { return true }}, q)
	if err != nil || got != "typed-on-screen" || asked != 1 {
		t.Fatalf("a presenter with a person: %q, %v, asked %d", got, err, asked)
	}
	// The file still comes first.
	file := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(file, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveEscrowPassphrase(EscrowFlags{PassphraseFile: file, ask: ask, atTerminal: func() bool { return true }}, q)
	if err != nil || got != "from-the-file" || asked != 1 {
		t.Fatalf("the file did not come first: %q, %v, asked %d", got, err, asked)
	}
	// And the environment before the person.
	t.Setenv(EscrowPassphraseEnv, "from-the-env")
	got, err = resolveEscrowPassphrase(EscrowFlags{ask: ask, atTerminal: func() bool { return true }}, q)
	if err != nil || got != "from-the-env" || asked != 1 {
		t.Fatalf("the environment did not come before the person: %q, %v, asked %d", got, err, asked)
	}
}

// The engine hands its presenter's word to the escrow stage: with stdin the null device
// (as under the terminal UI) and PersonAtTerminal set, the person is asked through the
// run's broker; without it, nobody is.
func TestTheEngineAsksThePersonItsPresenterVouchesFor(t *testing.T) {
	for _, vouched := range []bool{false, true} {
		isolateEscrow(t)
		orig := stdinIsTerminal
		stdinIsTerminal = func() bool { return false }
		t.Cleanup(func() { stdinIsTerminal = orig })
		var plan EscrowPlan
		e := &BootstrapEngine{Instance: "acme", SkipPreflight: true,
			Target: func(_ context.Context, _ Console, p EscrowPlan) (*State, error) {
				plan = p
				return nil, errors.New("stop here")
			},
		}
		if vouched {
			e.PersonAtTerminal = func() bool { return true }
		}
		r := runEngineRaw(t, e, installflow.Request{}, true, secretReply("typed-on-screen"), secretReply("typed-on-screen"))
		asked := len(r.replies.Questions())
		switch {
		case vouched && (asked != 2 || plan.Passphrase != "typed-on-screen"):
			t.Fatalf("vouched for: asked %d, passphrase %q, err %v", asked, plan.Passphrase, r.err)
		case !vouched && (asked != 0 || r.err == nil || !strings.Contains(r.err.Error(), "no terminal to ask at")):
			t.Fatalf("not vouched for: asked %d, err %v", asked, r.err)
		}
	}
}
