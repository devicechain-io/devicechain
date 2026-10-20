// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix && !windows

package test

import "testing"

// RefusingAddr is implemented on unix and windows only. Elsewhere it fails the test
// rather than hand back a port that merely used to be free.
func RefusingAddr(tb testing.TB) string {
	tb.Helper()
	tb.Fatal("holding a refusing address is implemented only on unix and windows")
	return ""
}
