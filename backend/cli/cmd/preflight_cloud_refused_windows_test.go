// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "syscall"

// Windows reports a refused connection as WSAECONNREFUSED; a reset is WSAECONNRESET and
// an unreachable network WSAENETUNREACH.
var (
	refusedOnThisOS    = []syscall.Errno{10061}
	notRefusedOnThisOS = []syscall.Errno{10054, 10051}
)
