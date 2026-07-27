// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"strings"
	"testing"
)

// The sslMode knob exists because A2 puts a real replication topology behind the
// database hostnames and "the link is plaintext" should not be un-changeable.
// These pin the two things that make it safe to have: it defaults to what every
// install was already doing, and it cannot be used to smuggle extra connection
// parameters into the DSN.

// An unset sslMode must keep the historical posture EXACTLY. If this test ever
// has to change, it is because someone altered the TLS posture of every existing
// install, which is a decision, not a refactor.
func TestAnUnsetSslModeKeepsTheHistoricalPosture(t *testing.T) {
	got, err := resolveSslMode("")
	if err != nil {
		t.Fatalf("resolveSslMode(\"\"): %v", err)
	}
	// Asserted against the literal, not against DefaultSslMode — comparing the
	// constant to itself would hold for any value it were changed to.
	if got != "disable" {
		t.Errorf("an unset sslMode resolved to %q, want %q — this silently changes "+
			"the TLS posture of every install that has not opted in", got, "disable")
	}
}

// Every mode libpq accepts must survive, or the knob is decoration: the whole
// point is being able to reach `require` and `verify-full`.
func TestEverySupportedSslModeIsAccepted(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			got, err := resolveSslMode(mode)
			if err != nil {
				t.Fatalf("resolveSslMode(%q): %v", mode, err)
			}
			if got != mode {
				t.Errorf("resolveSslMode(%q) = %q", mode, got)
			}
		})
	}
}

// The reason this is an allow-list and not a pass-through. The DSN is assembled
// as a space-separated keyword=value string, so a value containing whitespace
// appends connection parameters of the attacker's choosing — including ones that
// weaken or redirect TLS. A typo is the benign case; these are not.
func TestSslModeRefusesValuesThatWouldInjectDsnParameters(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
	}{
		{"a second parameter smuggled in", "disable sslrootcert=/tmp/attacker.crt"},
		{"downgrading while looking strict", "verify-full sslmode=disable"},
		{"redirecting the host", "require host=evil.internal"},
		{"a tab instead of a space", "disable\tsslcert=/tmp/x"},
		{"a newline", "disable\nsslkey=/tmp/x"},
		{"a plain typo", "requrie"},
		{"case that libpq would not accept either", "DISABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSslMode(tc.mode)
			if err == nil {
				t.Fatalf("resolveSslMode(%q) was accepted and resolved to %q", tc.mode, got)
			}
			if got != "" {
				t.Errorf("a value was returned alongside the error: %q", got)
			}
			// The error has to name the offending value AND the way out, or the
			// operator gets a fail-closed startup with nothing to act on.
			if !strings.Contains(err.Error(), "verify-full") {
				t.Errorf("the error does not list the permitted modes: %v", err)
			}
		})
	}
}

// Surrounding whitespace is a config-file artifact, not an attack, and should not
// fail a deploy. Note this is only safe BECAUSE the trimmed value is then checked
// against the allow-list — trimming a value we then passed through would be the
// worst of both.
func TestSslModeToleratesSurroundingWhitespace(t *testing.T) {
	got, err := resolveSslMode("  require\n")
	if err != nil {
		t.Fatalf("resolveSslMode: %v", err)
	}
	if got != "require" {
		t.Errorf("got %q, want %q", got, "require")
	}
}
