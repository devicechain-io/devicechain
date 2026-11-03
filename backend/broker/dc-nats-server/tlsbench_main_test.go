// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"
)

// TestMain lets tlsbench_test.go re-execute this binary as a broker node.
func TestMain(m *testing.M) {
	if os.Getenv("DCB_CHILD") == "1" {
		benchNodeMain()
		return
	}
	os.Exit(m.Run())
}
