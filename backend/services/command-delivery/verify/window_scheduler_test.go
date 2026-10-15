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
	  "ungated":["pump-c"]}}}`)
	holds, ungated, err := s.CurrentWindows(batchCtx(), []string{"pump-a", "pump-b", "pump-c"}, "reboot")
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
}

// An owner that answers without `ungated` (an older device-management) is an error, never
// "nothing is ungated", and never "nothing is held".
func TestCurrentWindowsFailsClosedOnAMissingList(t *testing.T) {
	for name, body := range map[string]string{
		"no ungated": `{"data":{"commandDispatchSchedule":{"holds":[]}}}`,
		"no holds":   `{"data":{"commandDispatchSchedule":{"ungated":["pump-a"]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newWindowSchedulerAgainst(t, body)
			if _, _, err := s.CurrentWindows(batchCtx(), []string{"pump-a"}, "reboot"); err == nil {
				t.Fatal("a response missing a list was read as a decision")
			}
		})
	}
}
