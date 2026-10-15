// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's test
// binary. Tests that read log output call captureLog, which switches collection on against
// this one sink rather than installing a logger of their own: the Client logs from paho's
// callback goroutines and the publisher's, which a per-test swap of the global would race.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs, the only moment writing the global
// logger is safe. See dctest.LogSink.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
