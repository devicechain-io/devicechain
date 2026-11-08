// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/ruletestrun"
)

// Execute exits with the status an error names, and 1 for any other error.
func TestExitCodeHonoursAnExitCoder(t *testing.T) {
	if got := exitCode(errors.New("plain")); got != 1 {
		t.Fatalf("plain error exits %d, want 1", got)
	}
	if got := exitCode(ruletestrun.Outcome(0, 1)); got != 2 {
		t.Fatalf("errored run exits %d, want 2", got)
	}
	if got := exitCode(ruletestrun.Outcome(1, 0)); got != 1 {
		t.Fatalf("failed run exits %d, want 1", got)
	}
	if got := exitCode(errors.Join(errors.New("context"), ruletestrun.Errored(errors.New("x")))); got != 2 {
		t.Fatalf("wrapped errored run exits %d, want 2", got)
	}
}

func withTestFlags(t *testing.T, tenant, email, password string) {
	t.Helper()
	oldT, oldE, oldP := testTenant, testEmail, testPassword
	testTenant, testEmail, testPassword = tenant, email, password
	t.Cleanup(func() { testTenant, testEmail, testPassword = oldT, oldE, oldP })
	for _, f := range []string{"junit", "server", "tls"} {
		if fl := testCmd.Flags().Lookup(f); fl != nil {
			_ = fl.Value.Set(fl.DefValue)
		}
	}
}

// A run that cannot load its files exits 2, writes the failure into the JUnit report a CI job
// reads, and reads the password from DCCTL_PASSWORD when --password is not given.
func TestRunTestLoadFailureIsAnErroredRunWithAJUnitReport(t *testing.T) {
	withTestFlags(t, "acme", "me@example.com", "")
	t.Setenv(passwordEnv, "from-env")
	report := filepath.Join(t.TempDir(), "junit.xml")
	if err := testCmd.Flags().Set("junit", report); err != nil {
		t.Fatal(err)
	}
	err := runTest(testCmd, []string{t.TempDir()}) // a directory with no test file
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "no") {
		t.Fatalf("err = %v (exit %d), want a load failure with exit status 2", err, exitCode(err))
	}
	b, rerr := os.ReadFile(report)
	if rerr != nil || !strings.Contains(string(b), `errors="1"`) || !strings.Contains(string(b), "load") {
		t.Fatalf("junit = %s (%v), want one errored case named load", b, rerr)
	}
}

// Without a password from either source the run is refused with exit status 2, before any file is read.
func TestRunTestNeedsAPassword(t *testing.T) {
	withTestFlags(t, "acme", "me@example.com", "")
	t.Setenv(passwordEnv, "")
	err := runTest(testCmd, []string{t.TempDir()})
	if exitCode(err) != 2 || !strings.Contains(err.Error(), passwordEnv) {
		t.Fatalf("err = %v, want a refusal naming %s with exit status 2", err, passwordEnv)
	}
}
