// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package cmd

import (
	"fmt"
	"os"
	"time"
)

// execPluginHelperOS is the helper plugin's OS-specific modes: none here.
func execPluginHelperOS(mode string, _ func(string, time.Time, string, string) int) int {
	fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
	return 2
}
