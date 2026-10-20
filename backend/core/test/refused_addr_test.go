// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix || windows

package test

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// A dial to the address is refused, and the address cannot be bound by anyone else while
// it is held: it is refusal by construction, not a port that happens to be free right now.
func TestRefusingAddrRefusesAndCannotBeTaken(t *testing.T) {
	addr := RefusingAddr(t)

	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		c.Close()
		t.Fatalf("dial %s succeeded; the address must refuse", addr)
	}
	// 10061 is WSAECONNREFUSED, which is what Windows reports (syscall.ECONNREFUSED is
	// not it there).
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.Errno(10061)) {
		t.Fatalf("dial %s failed, but not with a refusal: %v", addr, err)
	}

	if l, err := net.Listen("tcp", addr); err == nil {
		l.Close()
		t.Fatalf("bound %s while it was held; another process could take it the same way", addr)
	}
}

// The negative control: the same dial against a live listener is NOT refused, so the
// refusal above is a measurement and not the only answer the dial knows how to give.
func TestRefusingAddrControlALiveListenerIsNotRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial a live listener: %v", err)
	}
	c.Close()
}
