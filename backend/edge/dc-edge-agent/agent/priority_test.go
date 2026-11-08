// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"strings"
	"testing"

	dctest "github.com/devicechain-io/dc-microservice/test"

	"github.com/devicechain-io/dc-edge-agent/config"
)

func TestClassifierFirstMatchWinsAndDefaultsToStandard(t *testing.T) {
	c := newClassifier([]config.PriorityRule{
		{Match: "estop-*", Class: config.ClassSafety},
		{Match: "cam-*", Class: config.ClassBulk},
		{Match: "*", Class: config.ClassStandard},
	})
	for device, want := range map[string]int{"estop-1": classSafety, "cam-9": classBulk, "t-1": classStandard} {
		if got := c.classOf(device); got != want {
			t.Errorf("classOf(%q) = %d, want %d", device, got, want)
		}
	}
	if got := newClassifier(nil).classOf("anything"); got != classStandard {
		t.Errorf("no rules: classOf = %d, want standard", got)
	}
}

// priorityAgent holds the spool (no cloud, so nothing drains) with a low shed threshold.
func priorityAgent(t *testing.T) *Agent {
	t.Helper()
	cfg := config.Configuration{
		InstanceId: "test",
		Local: config.LocalConfiguration{
			ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t),
			SpoolMaxBytes: config.SpoolMinBytes,
			Priority: config.PriorityConfiguration{
				Rules: []config.PriorityRule{
					{Match: "estop-*", Class: config.ClassSafety},
					{Match: "cam-*", Class: config.ClassBulk},
				},
				ShedAtFraction: 0.05, ShedToFraction: 0.04,
			},
		},
		Uplink: config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://127.0.0.1:1", ConnectTimeoutSeconds: 1},
	}
	a := newAgent(t, cfg, nil)
	t.Cleanup(runReady(t, a))
	return a
}

func fill(t *testing.T, a *Agent, device string, n int) {
	t.Helper()
	body := fmt.Sprintf(`{"eventType":"measurement","values":{"blob":"%s"}}`, strings.Repeat("x", 60*1024))
	for i := 0; i < n; i++ {
		devicePublish(t, fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort), fmt.Sprintf("%s-%d", device, i),
			"test/tenant1/devices/"+device+"/events", []byte(body))
	}
}

func countDevice(t *testing.T, a *Agent, device string) int {
	t.Helper()
	info, err := a.js.StreamInfo(captureStream)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		raw, err := a.js.GetMsg(captureStream, seq)
		if err != nil {
			continue
		}
		if strings.HasSuffix(raw.Subject, ".devices."+device+".events") {
			n++
		}
	}
	return n
}

// TestShedsBulkBeforeStandardAndNeverSafety: the spool is over its threshold; the shedder frees
// room from the lowest class first, and the safety class survives however much it must shed.
func TestShedsBulkBeforeStandardAndNeverSafety(t *testing.T) {
	a := priorityAgent(t)
	// ~60 KiB each: 16 events are ~1 MiB against a 0.84 MiB threshold, and the bulk events
	// alone are enough to bring it back under. They are published FIRST, so shedding them
	// advances the spool's first sequence, which is what the drop metric reads.
	fill(t, a, "cam-1", 8)
	fill(t, a, "estop-1", 4)
	fill(t, a, "t-1", 4)

	shed := a.shedOnce()
	if shed[classBulk] == 0 {
		t.Fatalf("nothing shed from the bulk class: %v", shed)
	}
	if shed[classSafety] != 0 {
		t.Fatalf("safety events were shed: %v", shed)
	}
	if got := countDevice(t, a, "estop-1"); got != 4 {
		t.Fatalf("safety events remaining = %d, want 4", got)
	}
	// Enough bulk was shed to clear the threshold, so nothing of a higher class may have gone.
	if shed[classStandard] != 0 || countDevice(t, a, "t-1") != 4 {
		t.Fatalf("standard events shed while bulk events could cover it: %v", shed)
	}
	// A shed is not a ring eviction: the drop metric must not read it as one.
	a.sampleMetrics()
	if d := a.droppedTotal.Load(); d != 0 {
		t.Fatalf("dropped_total = %d after a deliberate shed, want 0: shed events are counted under shed_total, not as overflow", d)
	}
	if a.shedByClass[classBulk].Load() != int64(shed[classBulk]) {
		t.Fatalf("shed_total{bulk} = %d, want %d: a shed is counted, never silent", a.shedByClass[classBulk].Load(), shed[classBulk])
	}
}

// TestShedsStandardOnlyWhenBulkIsNotEnoughAndStillNeverSafety drives the threshold past what the
// bulk class can free.
func TestShedsStandardOnlyWhenBulkIsNotEnoughAndStillNeverSafety(t *testing.T) {
	a := priorityAgent(t)
	fill(t, a, "cam-1", 1)
	fill(t, a, "estop-1", 8)
	fill(t, a, "t-1", 8)

	shed := a.shedOnce()
	if shed[classStandard] == 0 {
		t.Fatalf("one bulk event cannot clear the threshold, so standard must shed: %v", shed)
	}
	if shed[classSafety] != 0 || countDevice(t, a, "estop-1") != 8 {
		t.Fatalf("safety events were lost: shed=%v remaining=%d", shed, countDevice(t, a, "estop-1"))
	}
	if countDevice(t, a, "cam-1") != 0 {
		t.Fatalf("bulk event survived while standard events were shed")
	}
}

func TestNoShedBelowTheThreshold(t *testing.T) {
	a := priorityAgent(t)
	fill(t, a, "cam-1", 1)
	if shed := a.shedOnce(); shed != [numClasses]int{} {
		t.Fatalf("shed %v below the threshold", shed)
	}
}

// TestShedReachesPastARetainedSafetyPrefix: a safety event anchors the head of the spool, and the
// scan window is smaller than the prefix. Restarting every pass at the first sequence would spend
// each pass's whole window on the same retained events and never reach the bulk events behind
// them; the cursor carries the scan forward, and standard events are not shed while bulk remain.
func TestShedReachesPastARetainedSafetyPrefix(t *testing.T) {
	old := shedScanLimit
	shedScanLimit = 3
	t.Cleanup(func() { shedScanLimit = old })
	a := priorityAgent(t)
	fill(t, a, "estop-1", 6) // the retained prefix, twice the window
	fill(t, a, "cam-1", 8)
	fill(t, a, "t-1", 2)

	total := [numClasses]int{}
	for pass := 0; pass < 20; pass++ {
		shed := a.shedOnce()
		for c := range total {
			total[c] += shed[c]
		}
	}
	if total[classBulk] == 0 {
		t.Fatalf("no bulk event was ever shed: the scan never got past the safety prefix (%v)", total)
	}
	if total[classSafety] != 0 || countDevice(t, a, "estop-1") != 6 {
		t.Fatalf("safety events were lost: %v", total)
	}
	if total[classStandard] != 0 || countDevice(t, a, "t-1") != 2 {
		t.Fatalf("standard events were shed while bulk events could still be: %v", total)
	}
}
