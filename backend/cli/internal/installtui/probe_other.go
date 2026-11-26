// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installtui

import (
	"io"
	"os"
)

// consoleSupportsVT is a Windows console question; elsewhere TERM answers it.
func consoleSupportsVT(*os.File) bool { return false }

// consoleReadsPastGate: outside Windows the program reads stdin through the gate.
func consoleReadsPastGate(io.Reader) bool { return false }
