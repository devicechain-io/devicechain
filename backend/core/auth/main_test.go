// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. A test that asserts on a log line switches collection on with
// logSink.Capture(t) rather than installing a logger of its own; dctest.LogSink says
// why a per-test swap of the global logger is a data race.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs, the one moment writing the global
// logger is safe. Nothing in this package reconfigures the logger afterwards, but the
// capturing tests still pair every negative assertion with a positive one, so a
// detached (empty) sink fails them rather than passing them.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
