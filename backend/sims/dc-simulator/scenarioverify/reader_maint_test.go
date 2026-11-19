// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

func maintenanceReader(t *testing.T, dispatch func(string, map[string]any) map[string]any) *Reader {
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
		switch {
		case strings.Contains(req.Query, "login("):
			data = map[string]any{"login": map[string]any{"identityToken": "identity"}}
		case strings.Contains(req.Query, "selectTenant("):
			data = map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": "2099-01-01T00:00:00Z"}}
		case strings.Contains(req.Query, "refresh("):
			data = map[string]any{"refresh": map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": "2099-01-01T00:00:00Z"}}
		default:
			data = dispatch(req.Query, req.Variables)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return &Reader{session: userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", testTenant), endpoints: sim.Endpoints{DeviceMgmtGraphQL: srv.URL, NotificationMgmtGraphQL: srv.URL}}
}

func TestMaintenanceReaderUsesPlatformEvidence(t *testing.T) {
	d := cleanDay()
	want := d.maint
	reads := map[string]int{}
	r := maintenanceReader(t, func(q string, v map[string]any) map[string]any {
		switch {
		case strings.Contains(q, "alarmSuppressionWindowsByToken("):
			reads["window"]++
			if !reflect.DeepEqual(v["tokens"], []any{testLampWindow}) {
				t.Errorf("wrong ledger token: %v", v)
			}
			return map[string]any{"alarmSuppressionWindowsByToken": []*AlarmWindow{want.Window}}
		case strings.Contains(q, "alarms("):
			c := v["c"].(map[string]any)
			dev := c["originatorToken"].(string)
			reads[dev]++
			if c["originatorType"] != "device" || c["alarmKey"] != sim.HarborpulseFlickerAlarmKey {
				t.Errorf("missing alarm filters: %v", c)
			}
			return map[string]any{"alarms": map[string]any{"results": []*LampCycle{want.Lamps[dev]}, "pagination": map[string]any{"totalRecords": 1}}}
		case strings.Contains(q, "notificationAttempts("):
			token := v["c"].(map[string]any)["alarmToken"].(string)
			reads[token]++
			var rows []map[string]any
			for _, a := range d.s3.Attempts[token] {
				rows = append(rows, map[string]any{"alarmToken": token, "attemptedAt": a.At, "trigger": a.Trigger, "status": a.Status, "channelToken": a.Channel})
			}
			return map[string]any{"notificationAttempts": map[string]any{"results": rows, "pagination": map[string]any{"totalRecords": len(rows)}}}
		default:
			t.Errorf("unexpected query: %s", q)
			return nil
		}
	})
	obs := d.observe()
	obs.Maint = Maintenance{}
	obs.S3.Attempts = map[string][]Attempt{}
	// Ledger times/device are deliberately wrong: only the token/day identify what to read.
	for i := range obs.S4.Jobs {
		if obs.S4.Jobs[i].Name == sim.HarborpulseJobLampService {
			obs.S4.Jobs[i].WindowOpens = "wrong"
			obs.S4.Jobs[i].Device = "wrong"
		}
	}
	if err := readMaintenance(context.Background(), r, &obs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(obs.Maint, want) {
		t.Fatalf("read-back changed platform evidence: %+v", obs.Maint)
	}
	if err := maintenanceHoldsPage(obs); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceControlPages(obs); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 5 {
		t.Fatalf("missing window/cycle/attempt read: %v", reads)
	}
}

func TestMaintenanceReaderRefusesPartialOrWrongRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]any
	}{
		{"null total", map[string]any{"results": []any{}, "pagination": map[string]any{}}},
		{"duplicate", map[string]any{"results": []any{}, "pagination": map[string]any{"totalRecords": 2}}},
		{"wrong device", map[string]any{"results": []map[string]any{{"token": "a", "originatorToken": "another", "alarmKey": "lamp-flicker"}}, "pagination": map[string]any{"totalRecords": 1}}},
		{"bad raisedTime", map[string]any{"results": []map[string]any{{"token": "a", "raisedTime": "invalid"}}, "pagination": map[string]any{"totalRecords": 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := maintenanceReader(t, func(string, map[string]any) map[string]any { return map[string]any{"alarms": tc.row} })
			if _, err := r.LampCycle(context.Background(), sim.HarborpulseMaintenanceLamp); err == nil {
				t.Fatal("invalid platform row accepted")
			}
		})
	}
}

func TestMaintenanceReaderPreservesMissingEvidence(t *testing.T) {
	r := maintenanceReader(t, func(q string, _ map[string]any) map[string]any {
		if strings.Contains(q, "alarmSuppressionWindowsByToken(") {
			return map[string]any{"alarmSuppressionWindowsByToken": []any{}}
		}
		return map[string]any{"alarms": map[string]any{"results": []any{}, "pagination": map[string]any{"totalRecords": 0}}}
	})
	if w, err := r.MaintenanceWindow(context.Background(), testLampWindow); w != nil || err != nil {
		t.Fatalf("missing window: %v %v", w, err)
	}
	if a, err := r.LampCycle(context.Background(), sim.HarborpulseControlLamp); a != nil || err != nil {
		t.Fatalf("missing cycle: %v %v", a, err)
	}
}
