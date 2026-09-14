// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package stalelayout is a fixture for TestTheDescribedPathGuardCanFail. Every
// reference below describes the layout that existed before instances were nested,
// and the Go tool never builds this directory.
package stalelayout

import "fmt"

// removeState reads ~/.devicechain/<instance> to find the state — a comment naming a
// path directly under the root, which is the carrier a literal-only guard misses.
func removeState(instance string) string {
	return fmt.Sprintf("removing local state (~/.devicechain/%s)", instance)
}

// theRootItselfIsFine mentions ~/.devicechain and ~/.devicechain/escrow, neither of
// which is stale, so a guard that flagged everything would report more than three.
func theRootItselfIsFine() string {
	return "kept root-key escrow in ~/.devicechain/escrow"
}

func namedInstance() string { return "~/.devicechain/prod/infra" }
