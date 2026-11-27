// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix && !linux && !darwin

package proctree

import "errors"

// waitExit has no way here to see an exit without reaping it, so Run ends a cancelled
// command's leader alone, as exec.CommandContext does.
func waitExit(int) error { return errors.New("no exit watch without reaping on this platform") }
