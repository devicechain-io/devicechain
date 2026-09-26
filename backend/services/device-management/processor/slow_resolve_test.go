// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

const slowResolveMessage = "Event resolution is slow"

// slowLines returns the captured slow-resolve lines, decoded.
func slowLines(t *testing.T, w warnCapture) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(w.String(), "\n") {
		if !strings.Contains(line, slowResolveMessage) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a captured log line is not JSON: %q", line)
		}
		out = append(out, entry)
	}
	return out
}

// testClock is a clock moved by hand.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

// The first slow resolve is logged at once, and after that at most one line per window,
// carrying how many there were, the slowest, and one to look up.
func TestASlowResolveIsLoggedOnceThenSummarised(t *testing.T) {
	logs := captureWarnings(t)
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	s := &slowResolveReporter{threshold: 10 * time.Millisecond, every: time.Second, now: clock.now}

	s.observe(20*time.Millisecond, "first")
	for i := 0; i < 4; i++ {
		s.observe(20*time.Millisecond, "later")
	}
	lines := slowLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("%d slow-resolve lines for 5 slow resolves inside one window, want exactly 1", len(lines))
	}
	if lines[0]["level"] != "warn" || lines[0]["correlation"] != "first" || lines[0]["slow"] != float64(1) {
		t.Errorf("the first line is %v, want level=warn slow=1 correlation=first", lines[0])
	}

	s.observe(5*time.Millisecond, "fast")
	if n := len(slowLines(t, logs)); n != 1 {
		t.Fatalf("a fast resolve inside the window logged; %d lines, want 1", n)
	}

	clock.t = clock.t.Add(time.Second)
	s.observe(30*time.Millisecond, "window-two")
	lines = slowLines(t, logs)
	if len(lines) != 2 {
		t.Fatalf("%d slow-resolve lines after the window closed, want 2", len(lines))
	}
	if lines[1]["slow"] != float64(5) || lines[1]["slowest"] != float64(30) {
		t.Errorf("the summary line is %v, want slow=5 (4 held over and this one) slowest=30ms", lines[1])
	}

	// Slow resolves counted at the end of a window are reported by the next resolve
	// after it, even a fast one, rather than waiting for another slow one that may
	// never come.
	s.observe(20*time.Millisecond, "held")
	clock.t = clock.t.Add(time.Second)
	s.observe(time.Millisecond, "fast")
	lines = slowLines(t, logs)
	if len(lines) != 3 || lines[2]["slow"] != float64(1) || lines[2]["correlation"] != "held" {
		t.Fatalf("slow-resolve lines = %v; want a third reporting the one held over (slow=1, correlation=held)", lines)
	}
}

// The resolver pool actually reports through it: a resolve that waits on a slow cache is
// logged, by the real Process loop.
func TestProcessReportsASlowResolve(t *testing.T) {
	rig := newCountingResolveRig(t, false, func(field string, kv *msgtest.MemoryKV) *messaging.Cache {
		return messaging.NewCacheOver(&hookedKV{MemoryKV: kv, after: func() { time.Sleep(30 * time.Millisecond) }})
	})
	logs := captureWarnings(t)

	encoded, err := esproto.MarshalUnresolvedEvent(tempEvent("21"))
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan messaging.Message, 1)
	resolvedCh := make(chan []EventResolutionResults, 1)
	rez := NewEventResolver(1, rig.capi, config.AuthModeDisabled, EventTimePolicy{}, in,
		func(error, messaging.Message) { t.Error("the event was reported invalid") },
		func(_ messaging.Message, _ string, r []EventResolutionResults) { resolvedCh <- r },
		nil, nil, nil)
	rez.slow.threshold = 10 * time.Millisecond

	in <- messaging.Message{Subject: "instance1.acme.inbound-events", Value: encoded, NumDelivered: 1}
	close(in)
	rez.Process(context.Background())

	select {
	case r := <-resolvedCh:
		if len(r) != 1 || r[0].Resolved.ProfileVersionToken != "p@1" {
			t.Fatalf("resolved %v, want one event at p@1", r)
		}
	default:
		t.Fatal("the event was not resolved")
	}
	if n := len(slowLines(t, logs)); n != 1 {
		t.Fatalf("%d slow-resolve lines for one resolve that waited on a slow cache, want exactly 1", n)
	}
}
