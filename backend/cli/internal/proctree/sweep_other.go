// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix && !linux

package proctree

import "context"

// sweep has no way here to read another process's environment, so it ends nothing, and
// says it looked at everything there is to see: there is nothing it could have found.
func sweep(context.Context, string) bool { return true }
