// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// refusedConnection is every error this OS reports a refused connection as. Windows
// reports one as WSAECONNREFUSED (10061): syscall.ECONNREFUSED is a value Go invents on
// Windows, which no Windows socket call returns, so matching it alone read every refused
// connection as UNKNOWN.
var refusedConnection = []error{windows.WSAECONNREFUSED, syscall.ECONNREFUSED}
