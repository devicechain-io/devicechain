// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// RunInSubprocess runs the current test in a fresh process so terminal cleanup
// cannot affect other tests. Call it before creating any fixture state:
//
//	if test.RunInSubprocess(t) { return }
//
// It returns true in the parent after the child passes, and false in the child.
func RunInSubprocess(t *testing.T) bool {
	t.Helper()
	const marker = "DC_TEST_SUBPROCESS"
	if os.Getenv(marker) == t.Name() {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), marker+"="+t.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated test failed: %v\n%s", err, output)
	}
	return true
}
