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

// ImpactsFor returns exactly the impacts the owner named, keyed by device, in one call.
func TestImpactsForRelaysTheOwnersClassification(t *testing.T) {
	s := newWindowSchedulerAgainst(t, `{"data":{"commandDispatchSchedule":{"impacts":[
	  {"deviceToken":"other","impact":"NORMAL"},{"deviceToken":"node-1","impact":"DISRUPTIVE"}]}}}`)
	got, err := s.ImpactsFor(batchCtx(), []string{"node-1", "other", "gone"}, "sparkplug.reboot")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["node-1"] != "DISRUPTIVE" || got["other"] != "NORMAL" {
		t.Fatalf("impacts = %v, want each device's own", got)
	}
	if _, ok := got["gone"]; ok {
		t.Fatal("a device the owner decided nothing about must be ABSENT, never NORMAL")
	}
}

// An empty impact is undecided, and a response that lost the field (an older owner) or the whole
// schedule is a failure, not an empty decision.
func TestImpactsForTreatsSilenceAsUndecidedAndALostFieldAsAFailure(t *testing.T) {
	s := newWindowSchedulerAgainst(t, `{"data":{"commandDispatchSchedule":{"impacts":[{"deviceToken":"node-1","impact":""}]}}}`)
	got, err := s.ImpactsFor(batchCtx(), []string{"node-1"}, "x")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty impact: %v, %v; want no entry and no error", got, err)
	}
	for name, body := range map[string]string{
		"field missing": `{"data":{"commandDispatchSchedule":{"holds":[],"refusals":[]}}}`,
		"no schedule":   `{"data":{"commandDispatchSchedule":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newWindowSchedulerAgainst(t, body)
			if got, err := s.ImpactsFor(batchCtx(), []string{"node-1"}, "x"); err == nil {
				t.Fatalf("got %v with no error; a lost field must be an error", got)
			}
		})
	}
}
