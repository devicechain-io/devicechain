// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// captureLog swaps the global logger for one writing into a buffer, for the test's lifetime.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

// gateOffWarnings counts the repeated gate-off WARN lines in a captured log.
func gateOffWarnings(buf *bytes.Buffer) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `"level":"warn"`) && strings.Contains(line, "presence gate is still OFF") {
			n++
		}
	}
	return n
}

// TestAnUngatedDispatchIsCountedAndWarned: with the gate switched off by missing configuration
// every command still dispatches (the safe default is unchanged), but each one is counted and
// the WARN naming the missing keys reaches the log again, not only at startup.
func TestAnUngatedDispatchIsCountedAndWarned(t *testing.T) {
	logs := captureLog(t)
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1"), queued(2, "c2")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.PresenceGateOff = "infrastructure.deviceState.port"
	proc.PresenceGateOffDispatches = prometheus.NewCounter(prometheus.CounterOpts{Name: "gate_off"})

	proc.sweepLocked(context.Background())

	if writer.count() != 2 {
		t.Fatalf("a switched-off gate must still dispatch every command, published %d", writer.count())
	}
	if got := testutil.ToFloat64(proc.PresenceGateOffDispatches); got != 2 {
		t.Fatalf("each command dispatched with the gate off must be counted, got %v", got)
	}
	if got := gateOffWarnings(logs); got != 1 {
		t.Fatalf("one pass must repeat the gate-off WARN exactly once, got %d:\n%s", got, logs)
	}
	if !strings.Contains(logs.String(), `"missing":"infrastructure.deviceState.port"`) {
		t.Fatalf("the WARN must name the missing key:\n%s", logs)
	}
}

// TestAWiredGateCountsNothing is the counterweight: a counter that ticks on every dispatch
// would satisfy the test above and make every instance read as ungated.
func TestAWiredGateCountsNothing(t *testing.T) {
	logs := captureLog(t)
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.PresenceGateOffDispatches = prometheus.NewCounter(prometheus.CounterOpts{Name: "gate_off"})

	proc.sweepLocked(context.Background())

	if writer.count() != 1 {
		t.Fatalf("the command must dispatch, published %d", writer.count())
	}
	if got := testutil.ToFloat64(proc.PresenceGateOffDispatches); got != 0 {
		t.Fatalf("a wired gate must count no ungated dispatches, got %v", got)
	}
	if got := gateOffWarnings(logs); got != 0 {
		t.Fatalf("a wired gate must not warn that it is off:\n%s", logs)
	}
}

// TestTheGateOffWarningRepeatsAtMostHourly: loud enough to stay on the page of log an operator
// reads, bounded so a busy fleet does not drown everything else in it. The counter is not
// rate-limited.
func TestTheGateOffWarningRepeatsAtMostHourly(t *testing.T) {
	logs := captureLog(t)
	proc := &CommandDeliveryProcessor{PresenceGateOff: "infrastructure.serviceAuth.secret"}
	proc.PresenceGateOffDispatches = prometheus.NewCounter(prometheus.CounterOpts{Name: "gate_off"})
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	proc.noteUngatedDispatch(t0)
	proc.noteUngatedDispatch(t0.Add(presenceGateOffWarnEvery - time.Minute))
	if got := gateOffWarnings(logs); got != 1 {
		t.Fatalf("inside one period the WARN must fire once, got %d", got)
	}
	proc.noteUngatedDispatch(t0.Add(presenceGateOffWarnEvery))
	if got := gateOffWarnings(logs); got != 2 {
		t.Fatalf("a period later the WARN must fire again, got %d", got)
	}
	if got := testutil.ToFloat64(proc.PresenceGateOffDispatches); got != 3 {
		t.Fatalf("every ungated dispatch must be counted, got %v", got)
	}
}
