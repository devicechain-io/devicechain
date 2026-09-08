// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. Tests that need to read log output call captureLogs, which switches
// collection on against this one sink rather than installing a logger of their own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs.
//
// That timing is the fix, not a detail: this package's connection handlers log from
// the NATS client's async callback goroutine, which outlives the test that opened the
// connection, so a per-test swap of the global logger writes it underneath a live
// reader. The reasoning in full is on dctest.LogSink.
//
// 🔴 One consequence to keep in mind when adding tests here. Several of the assertions
// in connection_events_test.go are negative — they require that some message was NOT
// logged — and a negative assertion passes vacuously against an empty capture. Nothing
// in this package detaches the sink today, but core/core's microservice bootstrap
// configures the global logger itself, so a test that builds a real microservice would
// detach it and turn those assertions into ones that cannot fail. Pair a negative
// assertion with a positive one whenever that becomes possible.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}

// No test in this package may write the global logger.
//
// The race a swap reintroduces is only visible under `go test -race`, which the
// required CI gates do not run — so without this check the next swap would be caught
// by nobody until someone ran the race detector by hand while chasing something else,
// and its report would name this package rather than whatever they were looking for.
//
// It covers assignment by name, a taken address, an in-place mutating method call and
// a dot-import; what it cannot see is written on AssertNoGlobalLoggerSwap, and the
// hole that matters most here is a swap performed by a helper in another package.
func TestNoTestSwapsTheGlobalLogger(t *testing.T) {
	dctest.AssertNoGlobalLoggerSwap(t, ".")
}
