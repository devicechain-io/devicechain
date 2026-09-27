// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
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
//
// It also stops the package's shared embedded JetStream server (shared_broker_test.go),
// which outlives every test and so has no test to clean it up.
func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	code := m.Run()
	if err := stopSharedBroker(); err != nil {
		fmt.Fprintf(os.Stderr, "processor tests: shared JetStream broker teardown: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
