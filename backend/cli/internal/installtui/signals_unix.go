// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installtui

import (
	"os"
	"syscall"
)

// interruptSignals are the signals the presenter takes over while the screen is up. Each
// one's default action would end the process with the terminal still raw: SIGINT and
// SIGTERM, SIGHUP when the terminal goes away, and SIGQUIT. Each counts as an interrupt.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}
