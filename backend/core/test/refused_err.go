// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// IsRefused reports whether err is a refused connection. 10061 is WSAECONNREFUSED, which
// Windows reports in place of syscall.ECONNREFUSED.
func IsRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061))
}

// RequireRefusing fails tb unless a dial to addr is REFUSED. It is the precondition for a
// test that hands addr to code under test which flattens its dial error (a client library
// that retries to its deadline, a classifier that rewrites the chain): the errno is gone
// by the time the test sees the result, so the test would pass just as well against a
// listener that accepts and closes. Asserting the fixture here makes that substitution fail.
func RequireRefusing(tb testing.TB, addr string) {
	tb.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		c.Close()
		tb.Fatalf("dial %s succeeded; the address must refuse", addr)
	}
	if !IsRefused(err) {
		tb.Fatalf("dial %s failed, but not with a refusal: %v", addr, err)
	}
}
