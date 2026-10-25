// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for this package's test binary. A test
// that reads log output calls logSink.Capture(t) rather than installing a logger of its own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs, the only moment at which writing
// zerolog's global logger is safe.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
