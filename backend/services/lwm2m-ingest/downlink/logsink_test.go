// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. Tests that read log output call captureDrainLogs, which switches
// collection on against this one sink rather than installing a logger of their own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs.
//
// That timing is the point, not a detail: it is the only moment at which writing
// zerolog's global logger is safe, because no test has started and so nothing is
// logging yet. The drain this package tests runs on shard workers in production, so a
// per-test swap of that global is a write racing reads it cannot be ordered against.
// The reasoning in full is on dctest.LogSink.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
