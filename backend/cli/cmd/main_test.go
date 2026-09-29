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
func TestMain(m *testing.M) { os.Exit(kubeisolation.Run(m)) }
