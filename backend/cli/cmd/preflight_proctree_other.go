// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix && !windows

package cmd

import "os/exec"

// startTree starts cmd. With neither process groups nor job objects to end its
// descendants with it, a cancelled command is killed alone, as exec.CommandContext does.
func startTree(cmd *exec.Cmd) (end func(), err error) {
	return func() {}, cmd.Start()
}
