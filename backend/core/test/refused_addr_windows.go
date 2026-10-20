// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package test

import (
	"fmt"
	"syscall"
	"testing"
)

// RefusingAddr returns a loopback "host:port" that REFUSES connections and that no other
// socket can bind while the test runs: a socket bound to it and never listening, held until
// tb ends. See the unix implementation for why a closed probe listener is not the same thing.
func RefusingAddr(tb testing.TB) string {
	tb.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		tb.Fatalf("hold a refusing address: socket: %v", err)
	}
	tb.Cleanup(func() { _ = syscall.Closesocket(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		tb.Fatalf("hold a refusing address: bind: %v", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		tb.Fatalf("hold a refusing address: getsockname: %v", err)
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		tb.Fatalf("hold a refusing address: getsockname returned %T, not an IPv4 address", sa)
	}
	return fmt.Sprintf("127.0.0.1:%d", in4.Port)
}
