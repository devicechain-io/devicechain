// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. A test that reads log output calls logSink.Capture(t), which switches
// collection on against this one sink rather than installing a logger of its own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs: the only moment writing zerolog's
// global logger is safe, because nothing is logging yet. The processor logs from
// goroutines that outlive the test that started them, so a per-test swap would write
// the global underneath a live reader. The reasoning in full is on dctest.LogSink.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
