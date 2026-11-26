// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"
	"syscall"
)

// interruptSignals are the signals the presenter takes over while the screen is up. Go
// delivers a console close, logoff or shutdown as SIGTERM.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
