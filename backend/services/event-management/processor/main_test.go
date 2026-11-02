// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for this package's test binary. A test that
// asserts on a log line switches collection on with logSink.Capture(t) rather than swapping the
// global logger itself, which is a data race (see dctest.LogSink).
var logSink *dctest.LogSink

func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
