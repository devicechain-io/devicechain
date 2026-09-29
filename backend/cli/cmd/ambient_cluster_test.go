// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/devicechain-io/dcctl/internal/kubeisolation"
)

// With KUBECONFIG naming a live API server, a cluster client built the way dcctl builds one
// in this package's test process reaches nothing. No test here reads a cluster today; this
// package links bootstrap, so any test added here can, and TestMain is what stops it.
func TestThisPackageRunsAgainstAnEmptyKubeconfig(t *testing.T) {
	kubeisolation.RequireIsolated(t)
}
