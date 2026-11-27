// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cmd

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processAlive reports whether pid is a running process. A zombie is not: in a container
// whose first process reaps nothing, a killed orphan stays a zombie for ever.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true // no /proc to say otherwise
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) && s[i+2] == 'Z' {
		return false
	}
	return true
}
