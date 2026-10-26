// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"errors"
	"net"
	"syscall"
)

// wsaeconnrefused is WSAECONNREFUSED, the winsock code a refused connect carries on
// Windows. There syscall.ECONNREFUSED is an invented constant that the real error never
// equals, so errors.Is(err, syscall.ECONNREFUSED) is false for a genuine refusal.
const wsaeconnrefused = syscall.Errno(10061)

// refusedAtDial reports whether err is a TCP connect the peer actively refused.
//
// 🔴 It is deliberately narrow, and only this case is DEFINITE: a refused connect never
// established a connection, so no request was written and the server cannot have
// stored the event. A reset or a timeout after the request was written, a dial that
// timed out, a DNS failure and a read error all leave the store's state unknown and
// stay ambiguous. It matches on the error TYPE (a *net.OpError whose Op is "dial"
// wrapping ECONNREFUSED), never on message text.
func refusedAtDial(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		return false
	}
	var errno syscall.Errno
	if !errors.As(op.Err, &errno) {
		return false
	}
	return errno == syscall.ECONNREFUSED || errno == wsaeconnrefused
}
