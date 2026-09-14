// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package exempt holds the two shapes the guard must NOT flag. Both are deliberate
// exemptions rather than gaps, and neither is visible in a passing live run — so
// they are pinned here instead.
package exempt

import "fmt"

// Prose names the directory to a human and builds nothing. It belongs in whichever
// file prints it, not in dcdir.
func Prose(instance string) string {
	return fmt.Sprintf("removing local state (~/.devicechain/%s)", instance)
}

// Domain is the near-miss that matters: an address containing the same characters
// without naming a path element at all.
func Domain(name string) string {
	return name + "@sim.devicechain.local"
}
