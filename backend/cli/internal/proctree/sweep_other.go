// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix && !linux

package proctree

// sweep has no way here to read another process's environment, so it ends nothing.
func sweep(string) {}
