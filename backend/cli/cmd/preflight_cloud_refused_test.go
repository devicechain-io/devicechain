// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"net"
	"os"
	"testing"
	"time"
)

// A connection the other end refuses reads as refused on every OS: a real dial to a port
// nothing listens on. On Windows the error is WSAECONNREFUSED (10061), which is not
// syscall.ECONNREFUSED there, and read UNKNOWN until the classifier knew it.
func TestARefusedConnectionIsRefusedOnEveryOS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("tcp", addr)
	if err == nil {
		_ = c.Close()
		t.Skip("something took the port between the close and the dial")
	}
	if got := classifyProbeErr(err); got != probeRefused {
		t.Fatalf("a refused dial (%v) classifies as %v, want refused", err, got)
	}
}

// The classifier itself, on this OS's errors: each refusal the OS reports, wrapped as a
// dial wraps it, is refused, and its neighbours (a reset, an unreachable network) are
// not. refusedOnThisOS and notRefusedOnThisOS are per-OS (a _windows_test.go file).
func TestTheClassifierKnowsThisOSsRefusal(t *testing.T) {
	wrap := func(e error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", e)}
	}
	for _, e := range refusedOnThisOS {
		if got := classifyProbeErr(wrap(e)); got != probeRefused {
			t.Errorf("%v (%d) classifies as %v, want refused", e, e, got)
		}
	}
	for _, e := range notRefusedOnThisOS {
		if got := classifyProbeErr(wrap(e)); got == probeRefused {
			t.Errorf("%v (%d) classifies as refused", e, e)
		}
	}
}
