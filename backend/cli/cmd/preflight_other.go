// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package cmd

// checkSystem is a no-op stub on non-Linux hosts. The kernel/cgroup/disk/WSL
// diagnostics rely on Linux /proc and statfs semantics; the DeviceChain runtime
// target is Linux, so run `dcctl preflight` inside the Linux/WSL2 dev
// environment for the full host diagnostics.
func checkSystem(d *doctor, _ string) {
	section("System", "🖥️")
	d.warn("host diagnostics skipped on non-Linux host",
		"run dcctl preflight inside the Linux/WSL2 dev environment for full system checks")
}
