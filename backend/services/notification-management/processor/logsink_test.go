// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. Tests that read log output call captureLogs, which switches collection
// on against this one sink rather than installing a logger of their own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs.
//
// That timing is the point, not a detail: it is the only moment at which writing
// zerolog's global logger is safe, because no test has started and so nothing is
// logging yet. A dispatch whose whole-dispatch budget is cut abandons its in-flight
// channel goroutines rather than waiting for them — that is what the budget is for —
// so a per-test swap restored in t.Cleanup writes that global while an abandoned
// goroutine is still reading it. The reasoning in full is on dctest.LogSink.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
