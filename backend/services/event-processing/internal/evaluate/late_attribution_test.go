// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"reflect"
	"testing"
	"time"
)

func TestLateAttributionPreservesTotals(t *testing.T) {
	p := params([]Rule{hotRule(alarmAction())}, []Event{reading("on-time", t0, "temperature", 70), reading("late-device", t0.Add(10*time.Minute), "temperature", 90), reading("late-device", t0.Add(time.Minute), "temperature", 70)})
	plain := mustRun(t, p)
	p.AttributeLate = true
	attributed := mustRun(t, p)
	if attributed.Stats.LateReadings == 0 || attributed.LateByDevice["late-device"] != attributed.Stats.LateReadings || len(attributed.LateByDevice) != 1 {
		t.Fatalf("%+v", attributed)
	}
	if !reflect.DeepEqual(plain.Stats, attributed.Stats) || !reflect.DeepEqual(plain.Degradations, attributed.Degradations) || plain.Degraded != attributed.Degraded || !reflect.DeepEqual(plain.Edges, attributed.Edges) || !reflect.DeepEqual(plain.Intents, attributed.Intents) {
		t.Fatal("attribution changed evaluation")
	}
}
