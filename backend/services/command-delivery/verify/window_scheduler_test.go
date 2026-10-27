// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newWindowSchedulerAgainst(t *testing.T, body string) *WindowScheduler {
	t.Helper()
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(dm.Close)
	return NewWindowScheduler(newSvcClient(t), dm.URL)
}

// The current-window answer relays both lists: the re-point step moves a device only on a hold
// naming another window, and releases one only when it is named ungated.
func TestCurrentWindowsRelaysHoldsAndUngated(t *testing.T) {
	s := newWindowSchedulerAgainst(t, `{"data":{"commandDispatchSchedule":{
	  "holds":[{"deviceToken":"pump-a","windowToken":"mw-b","dispatchAfter":"2026-01-17T01:00:00Z","dispatchUntil":"2026-01-17T05:00:00Z"}],
	  "ungated":["pump-c"],"undefined":["pump-d"]}}}`)
	holds, ungated, undefined, err := s.CurrentWindows(batchCtx(), []string{"pump-a", "pump-b", "pump-c", "pump-d"}, "reboot")
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 1 || holds[0].DeviceToken != "pump-a" || holds[0].WindowToken != "mw-b" ||
		!holds[0].DispatchAfter.Equal(time.Date(2026, 1, 17, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("holds = %+v", holds)
	}
	if len(ungated) != 1 || ungated[0] != "pump-c" {
		t.Fatalf("ungated = %v, want [pump-c]", ungated)
	}
	if len(undefined) != 1 || undefined[0] != "pump-d" {
		t.Fatalf("undefined = %v, want [pump-d]", undefined)
	}
}

// An owner that answers without `ungated` (an older device-management) is an error, never
// "nothing is ungated", and never "nothing is held".
func TestCurrentWindowsFailsClosedOnAMissingList(t *testing.T) {
	for name, body := range map[string]string{
		"no ungated":   `{"data":{"commandDispatchSchedule":{"holds":[],"undefined":[]}}}`,
		"no holds":     `{"data":{"commandDispatchSchedule":{"ungated":["pump-a"],"undefined":[]}}}`,
		"no undefined": `{"data":{"commandDispatchSchedule":{"holds":[],"ungated":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newWindowSchedulerAgainst(t, body)
			if _, _, _, err := s.CurrentWindows(batchCtx(), []string{"pump-a"}, "reboot"); err == nil {
				t.Fatal("a response missing a list was read as a decision")
			}
		})
	}
}

// CommandImpact returns the impact the owner named for exactly the device asked about.
func TestCommandImpactRelaysTheOwnersClassification(t *testing.T) {
	s := newWindowSchedulerAgainst(t, `{"data":{"commandDispatchSchedule":{"impacts":[
	  {"deviceToken":"other","impact":"NORMAL"},{"deviceToken":"node-1","impact":"DISRUPTIVE"}]}}}`)
	got, err := s.CommandImpact(batchCtx(), "node-1", "sparkplug.reboot")
	if err != nil || got != "DISRUPTIVE" {
		t.Fatalf("impact = %q, err %v; want DISRUPTIVE for the device asked about, not its neighbour's", got, err)
	}
}

// 🔴 SILENCE IS NEVER NORMAL. The owner lists impacts only for enabled devices whose type defines
// the command, so an empty list, a list without the device, an empty impact, and a response that
// lost the field (an older owner) are all failures to decide. Reading any of them as NORMAL would
// let an adapter that gates on impact treat the unknown as the safe class.
func TestCommandImpactFailsClosedWhenTheOwnerDecidedNothing(t *testing.T) {
	for name, body := range map[string]string{
		"empty list":        `{"data":{"commandDispatchSchedule":{"impacts":[]}}}`,
		"other device only": `{"data":{"commandDispatchSchedule":{"impacts":[{"deviceToken":"other","impact":"NORMAL"}]}}}`,
		"empty impact":      `{"data":{"commandDispatchSchedule":{"impacts":[{"deviceToken":"node-1","impact":""}]}}}`,
		"field missing":     `{"data":{"commandDispatchSchedule":{"holds":[],"refusals":[]}}}`,
		"no schedule":       `{"data":{"commandDispatchSchedule":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newWindowSchedulerAgainst(t, body)
			if got, err := s.CommandImpact(batchCtx(), "node-1", "sparkplug.reboot"); err == nil {
				t.Fatalf("got impact %q with no error; an undecided impact must be an error", got)
			}
		})
	}
}
