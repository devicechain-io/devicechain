// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package test

import (
	"fmt"
	"syscall"
)

// refusingAddr is a loopback TCP address held by a socket that is bound and never
// listens. A connect to it is refused (the kernel answers it with a reset), and no other
// socket can bind it while it is held: the socket does not set SO_REUSEADDR, which a
// second bind to the same address needs on both sockets.
type refusingAddr struct {
	addr string
	fd   int
}

// holdRefusingAddr binds a socket to a loopback port the operating system picks, and
// holds it without listening.
func holdRefusingAddr() (*refusingAddr, error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind: %w", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("getsockname: %w", err)
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		syscall.Close(fd)
		return nil, fmt.Errorf("getsockname returned %T, not an IPv4 address", sa)
	}
	return &refusingAddr{addr: fmt.Sprintf("127.0.0.1:%d", in4.Port), fd: fd}, nil
}

func (r *refusingAddr) close() { syscall.Close(r.fd) }
