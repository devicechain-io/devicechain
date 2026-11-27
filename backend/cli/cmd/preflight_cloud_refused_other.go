// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package cmd

import "syscall"

// refusedConnection is every error this OS reports a refused connection as.
var refusedConnection = []error{syscall.ECONNREFUSED}
