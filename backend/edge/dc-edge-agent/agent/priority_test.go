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
				ShedAtFraction: 0.05, ShedToFraction: 0.02,
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
	// ~60 KiB each: 6 safety, 6 standard, 6 bulk is ~2 MiB against a 0.8 MiB threshold.
	fill(t, a, "estop-1", 6)
	fill(t, a, "t-1", 6)
	fill(t, a, "cam-1", 6)

	shed := a.shedOnce()
	if shed[classBulk] == 0 {
		t.Fatalf("nothing shed from the bulk class: %v", shed)
	}
	if shed[classSafety] != 0 {
		t.Fatalf("safety events were shed: %v", shed)
	}
	if got := countDevice(t, a, "estop-1"); got != 6 {
		t.Fatalf("safety events remaining = %d, want 6", got)
	}
	// Bulk is exhausted before standard is touched.
	if shed[classStandard] > 0 && countDevice(t, a, "cam-1") != 0 {
		t.Fatalf("standard events shed while bulk events remained: %v", shed)
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
	fill(t, a, "estop-1", 8)
	fill(t, a, "t-1", 8)
	fill(t, a, "cam-1", 1)

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
