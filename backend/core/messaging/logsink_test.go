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

// The check that no test in this package writes the global logger is no longer here.
// It used to be, pointed at this one directory, which is precisely why three other
// packages went on swapping the logger unnoticed: a guard someone has to point at a
// package covers the packages someone remembered. It now walks the whole workspace
// from core/test — TestNoTestInTheRepositorySwapsTheGlobalLogger — so this package is
// covered by the same run that covers every other, including ones not yet written.
