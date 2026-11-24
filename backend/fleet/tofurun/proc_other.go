// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package tofurun

import (
	"os/exec"
	"time"
)

// controlProcess has no process groups to manage off Unix: the context's own kill applies. The
// runner image is Linux; this exists so the package builds on a developer's machine.
func controlProcess(*exec.Cmd, time.Duration) (stop func()) { return func() {} }
