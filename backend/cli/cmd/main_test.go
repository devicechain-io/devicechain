// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"testing"

	"github.com/devicechain-io/dcctl/internal/kubeisolation"
)

// TestMain isolates this package's tests from every cluster the environment names. The
// package links bootstrap, so its tests can reach any cluster bootstrap can.
//
// Run with execPluginHelperEnv set, the binary is instead the credential plugin the
// advisory checks' tests run (execPluginHelper), and runs no test.
func TestMain(m *testing.M) {
	if mode := os.Getenv(execPluginHelperEnv); mode != "" {
		os.Exit(execPluginHelper(mode))
	}
	os.Exit(kubeisolation.Run(m))
}
