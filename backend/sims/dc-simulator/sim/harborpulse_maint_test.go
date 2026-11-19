// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

func TestHarborLampServicePlan(t *testing.T) {
	started := time.Date(2026, 11, 16, 12, 0, 17, 123456789, time.UTC)
	for _, defect := range append([]string{""}, HarborpulseDefects()...) {
		t.Run("defect="+defect, func(t *testing.T) {
			var defects []string
			if defect != "" {
				defects = []string{defect}
			}
			s := harborWith(t, defects...)
			job, request := s.lampService(started, 2)
			want := HarborpulseMaintenanceLamp
			if defect == HarborpulseDefectSuppressionWrongDevice {
				want = "hp-light-east-2"
			}
			if job.Device != want || !reflect.DeepEqual(request["targets"], []any{map[string]any{"type": "DEVICE", "token": want}}) {
				t.Fatalf("%s: wrong target: %+v / %v", defect, job, request["targets"])
			}
			if job.Window != fmt.Sprintf("hp-lamp-service-%d-d2", started.Unix()) || request["kind"] != "ONE_SHOT" || request["reason"] == "" || request["bypassSeverity"] != nil {
				t.Fatalf("invalid window request: %v", request)
			}
			if !reflect.DeepEqual(request["alarmKeys"], []string{HarborpulseFlickerAlarmKey}) {
				t.Fatalf("keys: %v", request)
			}
			for field, hour := range map[string]float64{"startTime": 10.75, "endTime": 11.5} {
				got, err := time.Parse(time.RFC3339Nano, request[field].(string))
				if err != nil || !got.Equal(harborWall(started, s.dayLength, 2, hour)) {
					t.Fatalf("%s rounded or wrong: %v %v", field, got, err)
				}
			}
		})
	}
}

// Replay actual model readings with the default cadence through applyRepeating's strict cutoff.
// Include aligned and offset phases: real ticks need not land on a sim minute boundary.
func TestHarborLampMaintenanceTiming(t *testing.T) {
	r := harborRules(t)[HarborpulseFlickerRule]
	need, window, level := int(r.num(t, "count")), r.dur(t, "window"), r.num(t, "when", "threshold")
	for _, device := range []string{HarborpulseMaintenanceLamp, HarborpulseControlLamp} {
		for _, phase := range []time.Duration{0, time.Second, 4 * time.Second} {
			t.Run(fmt.Sprintf("%s/phase=%s", device, phase), func(t *testing.T) {
				model := newHarborModel(1)
				var matches []time.Duration
				var raised, fell time.Duration
				off := 0
				closeAt := time.Duration((11.5 - harborpulseStartHour) * float64(time.Minute))
				heldAt75 := false
				for elapsed := time.Duration((10.75-harborpulseStartHour)*float64(time.Minute)) + phase; elapsed <= time.Duration((13-harborpulseStartHour)*float64(time.Minute)); elapsed += (Load{}).Interval() {
					values := model.reading(device, elapsed, time.Unix(0, 0).Add(elapsed))
					if elapsed >= time.Duration((11-harborpulseStartHour)*float64(time.Minute)) && elapsed < time.Duration((12-harborpulseStartHour)*float64(time.Minute)) && values[HarborpulseLampFaultKey] <= level {
						off++
					}
					var kept []time.Duration
					for _, at := range matches {
						if at > elapsed-window {
							kept = append(kept, at)
						}
					}
					matches = kept
					if values[HarborpulseLampFaultKey] > level {
						matches = append(matches, elapsed)
					}
					active := len(matches) >= need
					if active && raised == 0 {
						raised = elapsed
					}
					if !active && raised != 0 && fell == 0 {
						fell = elapsed
					}
					// At +75, the previous event determines the level if this event lies after that instant.
					if elapsed <= closeAt+75*time.Second {
						heldAt75 = active
					}
				}
				if off != 1 {
					t.Errorf("intermittent beat has %d off readings, want exactly one at 11:20-11:25", off)
				}
				if raised == 0 || raised > closeAt-15*time.Second {
					t.Fatalf("raised at %s; need at least 15s before window close %s", raised, closeAt)
				}
				if fell < closeAt+75*time.Second || fell >= closeAt+80*time.Second {
					t.Fatalf("fell at %s; want [close+75s,close+80s)", fell)
				}
				// At phase zero the strict cutoff resolves at exactly +75; all offset phases stay active
				// at +75. Every phase outlives the default 60s release sweep plus its margin.
				if phase != 0 && !heldAt75 {
					t.Fatal("alarm must still be active at window close +75s")
				}
			})
		}
	}
}

func lampTestRuntime(t *testing.T, dispatch func(string, map[string]any) map[string]any) *Runtime {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var data map[string]any
		if strings.Contains(req.Query, "login(") {
			data = map[string]any{"login": map[string]any{"identityToken": "identity"}}
		} else if strings.Contains(req.Query, "selectTenant(") || strings.Contains(req.Query, "refresh(") {
			field := "selectTenant"
			if strings.Contains(req.Query, "refresh(") {
				field = "refresh"
			}
			data = map[string]any{field: map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": "2099-01-01T00:00:00Z"}}
		} else {
			data = dispatch(req.Query, req.Variables)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return &Runtime{Endpoints: Endpoints{DeviceMgmtGraphQL: srv.URL}, Session: userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme")}
}

func TestHarborLampServiceScheduling(t *testing.T) {
	s := harborWith(t)
	s.started = time.Date(2026, 11, 16, 12, 0, 17, 123456789, time.UTC)
	creates, deletes := 0, []string{}
	fail := true
	rt := lampTestRuntime(t, func(q string, v map[string]any) map[string]any {
		switch {
		case strings.Contains(q, "createAlarmSuppressionWindow("):
			creates++
			if fail {
				return map[string]any{"createAlarmSuppressionWindow": nil}
			}
			return map[string]any{"createAlarmSuppressionWindow": map[string]any{"token": v["request"].(map[string]any)["token"]}}
		case strings.Contains(q, "deleteAlarmSuppressionWindow("):
			deletes = append(deletes, v["token"].(string))
			return map[string]any{"deleteAlarmSuppressionWindow": true}
		default:
			t.Errorf("unexpected query: %s", q)
			return nil
		}
	})
	now := harborWall(s.started, s.dayLength, 0, HarborpulseLampServiceFrom).Add(-15 * time.Second)
	if err := s.scheduleMaintenance(context.Background(), rt, now); err == nil {
		t.Fatal("missing create response silently accepted")
	}
	if len(s.jobs) != 0 || s.maintenanceScheduled[0] {
		t.Fatal("failed create ledgered or marked scheduled")
	}
	fail = false
	if err := s.scheduleMaintenance(context.Background(), rt, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleMaintenance(context.Background(), rt, now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	if creates != 2 || len(s.jobs) != 1 {
		t.Fatalf("retry/idempotence: creates=%d jobs=%v", creates, s.jobs)
	}
	if err := s.scheduleMaintenance(context.Background(), rt, harborWall(s.started, s.dayLength, 3, 6)); err != nil {
		t.Fatal(err)
	}
	if len(deletes) != 1 || deletes[0] != s.jobs[0].Window {
		t.Fatalf("old day not pruned: %v", deletes)
	}
	// Campaign deadline (14:00) must not schedule a window whose 10:45 opening was missed.
	if err := s.scheduleMaintenance(context.Background(), rt, harborWall(s.started, s.dayLength, 4, 11)); err != nil {
		t.Fatal(err)
	}
	if len(s.jobs) != 2 || !s.maintenanceScheduled[4] {
		t.Fatal("late day was not independently skipped")
	}
}

func TestHarborLampWindowCleanup(t *testing.T) {
	pages, deleted := 0, []string{}
	rt := lampTestRuntime(t, func(q string, v map[string]any) map[string]any {
		if strings.Contains(q, "alarmSuppressionWindows(") {
			pages++
			var results []map[string]any
			n := int(v["c"].(map[string]any)["pageNumber"].(float64))
			for i := (n - 1) * 100; i < n*100 && i < 102; i++ {
				token := fmt.Sprintf("hp-lamp-service-old-%d", i)
				if i == 0 {
					token = "operator-window"
				}
				results = append(results, map[string]any{"token": token})
			}
			return map[string]any{"alarmSuppressionWindows": map[string]any{"results": results, "pagination": map[string]any{"totalRecords": 102}}}
		}
		if pages != 2 {
			t.Error("deleting before reading every page shifts pagination")
		}
		deleted = append(deleted, v["token"].(string))
		return map[string]any{"deleteAlarmSuppressionWindow": true}
	})
	if err := deleteHarborLampWindows(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(deleted) != 101 {
		t.Fatalf("cleanup pages=%d deleted=%d", pages, len(deleted))
	}
	for _, token := range deleted {
		if token == "operator-window" {
			t.Fatal("deleted operator window")
		}
	}
}
