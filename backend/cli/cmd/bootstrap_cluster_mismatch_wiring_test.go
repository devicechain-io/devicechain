// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"strings"
	"testing"
)

// The stale-state refusal is tested in the bootstrap package; this holds that the command
// ASKS, and asks before the instance record is overwritten with this cluster's identity
// (after which the evidence of the mismatch is gone).
func TestBootstrapRefusesLocalStateFromAnotherClusterBeforeRewritingTheRecord(t *testing.T) {
	b, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	ask := strings.Index(src, "bootstrap.RefuseInstanceOnAnotherCluster(")
	write := strings.Index(src, "bootstrap.WriteInstanceRecord(")
	if ask < 0 {
		t.Fatal("dcctl bootstrap no longer calls RefuseInstanceOnAnotherCluster")
	}
	if write < 0 || ask > write {
		t.Fatal("RefuseInstanceOnAnotherCluster must run before WriteInstanceRecord")
	}
}
