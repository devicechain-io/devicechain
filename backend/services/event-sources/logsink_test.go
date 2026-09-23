// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's test
// binary. A test that reads log output calls logSink.Capture(t) rather than installing a
// logger of its own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs — the only moment at which writing
// zerolog's global logger is safe, because nothing is logging yet. Several tests here
// start goroutines that log on their own schedule (the presence loops, the demote loop,
// failProcess), so a per-test swap of that global would race them. The reasoning in full
// is on dctest.LogSink.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
