// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// logSink is where the global zerolog logger writes for the whole of this package's
// test binary. Tests that read log output call captureDebugLog, which switches
// collection on against this one sink rather than installing a logger of their own.
var logSink *dctest.LogSink

// TestMain installs the sink before any test runs.
//
// That timing is the point, not a detail: it is the only moment at which writing
// zerolog's global logger is safe, because no test has started and so nothing is
// logging yet. An MQTT source receives on the paho client's own callback goroutine,
// which runs on its own schedule and outlives the call that subscribed, so a per-test
// swap of that global writes it underneath a live reader. The reasoning in full is on
// dctest.LogSink.
//
// 🔴 One thing to keep in mind when adding tests here. The redaction assertion in
// mqtt_test.go is negative — a payload must NOT appear in the log — and a negative
// assertion passes vacuously against an empty capture. Nothing in this package
// detaches the sink today, but the microservice bootstrap in core configures the
// global logger itself, so a test that builds a real Microservice would detach it from
// that point on. That assertion is already paired with positive ones for the same
// reason; keep it that way.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
