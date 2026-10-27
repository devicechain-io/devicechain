// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

type breakGlassCall struct {
	kubeContext, instance, email string
	stdin                        io.Reader
}

func stubBreakGlass(t *testing.T) *[]breakGlassCall {
	t.Helper()
	var calls []breakGlassCall
	orig := breakGlassReset
	t.Cleanup(func() { breakGlassReset = orig })
	breakGlassReset = func(_ context.Context, kubeContext, instance, email string, stdin io.Reader, _, _ io.Writer) error {
		calls = append(calls, breakGlassCall{kubeContext, instance, email, stdin})
		return nil
	}
	return &calls
}

// A reset with no terminal to confirm on and no --yes is REFUSED and runs nothing: the same rule
// the other destructive commands follow.
func TestBreakGlassNeedsAConfirmationOrYes(t *testing.T) {
	withTerminal(t, false, false)
	calls := stubBreakGlass(t)
	err := doBreakGlassMfaReset(context.Background(), io.Discard, io.Discard, strings.NewReader(""),
		"acme", "root@example.com", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("ran %d time(s) without confirmation", len(*calls))
	}

	// --yes is the deliberate way, and it works without a terminal.
	if err := doBreakGlassMfaReset(context.Background(), io.Discard, io.Discard, strings.NewReader(""),
		"acme", "root@example.com", "kind-acme", false, true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].email != "root@example.com" || (*calls)[0].kubeContext != "kind-acme" {
		t.Errorf("calls = %+v", *calls)
	}
	if (*calls)[0].stdin != nil {
		t.Error("no password was asked for on stdin, but the call carries a stdin")
	}
}

// On a terminal the operator types the instance name back; anything else changes nothing.
func TestBreakGlassConfirmationMustMatchTheInstance(t *testing.T) {
	withTerminal(t, true, false)
	calls := stubBreakGlass(t)
	var out bytes.Buffer
	err := doBreakGlassMfaReset(context.Background(), &out, io.Discard, strings.NewReader("other\n"),
		"acme", "root@example.com", "", false, false)
	if err == nil || len(*calls) != 0 {
		t.Fatalf("err=%v calls=%d for a wrong confirmation", err, len(*calls))
	}
	if err := doBreakGlassMfaReset(context.Background(), &out, io.Discard, strings.NewReader("acme\n"),
		"acme", "root@example.com", "", false, false); err != nil || len(*calls) != 1 {
		t.Fatalf("err=%v calls=%d for the right confirmation", err, len(*calls))
	}
}

// --password-stdin and a confirmation prompt would both read stdin; the combination is refused
// unless --yes says the prompt is not wanted.
func TestBreakGlassPasswordStdinNeedsYes(t *testing.T) {
	withTerminal(t, true, false)
	calls := stubBreakGlass(t)
	if err := doBreakGlassMfaReset(context.Background(), io.Discard, io.Discard, strings.NewReader("pw\n"),
		"acme", "root@example.com", "", true, false); err == nil {
		t.Fatal("--password-stdin without --yes was accepted")
	}
	if err := doBreakGlassMfaReset(context.Background(), io.Discard, io.Discard, strings.NewReader("pw\n"),
		"acme", "root@example.com", "", true, true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].stdin == nil {
		t.Errorf("calls = %+v; the password stream must be handed through", *calls)
	}
}

func TestBreakGlassRequiresInstanceAndEmail(t *testing.T) {
	calls := stubBreakGlass(t)
	for _, c := range []struct{ instance, email string }{{"", "a@b.c"}, {"acme", ""}} {
		if err := doBreakGlassMfaReset(context.Background(), io.Discard, io.Discard, strings.NewReader(""),
			c.instance, c.email, "", false, true); err == nil {
			t.Errorf("accepted instance=%q email=%q", c.instance, c.email)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("ran %d time(s)", len(*calls))
	}
}
