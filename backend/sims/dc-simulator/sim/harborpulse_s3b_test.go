// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/payloadencode"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/lorawan"
)

func TestHarborpulseS3bBeatsAndDefects(t *testing.T) {
	for _, defect := range []string{"", HarborpulseDefectAttributeNotUpdated, HarborpulseDefectDownlinkNack, HarborpulseDefectBackfillWrongWindow} {
		t.Run(defect, func(t *testing.T) {
			s := NewHarborpulse(1, Load{}).(*harborpulse)
			if defect != "" {
				if err := s.ConfigureScenario(ScenarioOptions{Defects: []string{defect}}); err != nil {
					t.Fatal(err)
				}
			}
			s.started = time.Date(2026, 11, 18, 12, 0, 0, 123456789, time.UTC)
			attrs := map[string]string{}
			writes, commands, jobs := 0, 0, 0
			var job map[string]any
			var metadata []string
			auth := &fakeProfileServer{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query     string
					Variables map[string]json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				data := map[string]any{}
				switch {
				case strings.Contains(req.Query, "refresh("):
					data["refresh"] = map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
				case strings.Contains(req.Query, "entityAttributes("):
					var c struct{ Entity string }
					_ = json.Unmarshal(req.Variables["c"], &c)
					rows := []any{}
					if v, ok := attrs[c.Entity]; ok {
						rows = append(rows, map[string]any{"value": v, "valueType": "DOUBLE"})
					}
					data["entityAttributes"] = map[string]any{"results": rows}
				case strings.Contains(req.Query, "setEntityAttribute("):
					var a struct{ Entity, Value string }
					_ = json.Unmarshal(req.Variables["request"], &a)
					attrs[a.Entity] = a.Value
					writes++
					data["setEntityAttribute"] = map[string]any{"value": a.Value}
				case strings.Contains(req.Query, "updateDevice("):
					var a struct{ Metadata string }
					_ = json.Unmarshal(req.Variables["request"], &a)
					metadata = append(metadata, a.Metadata)
					data["updateDevice"] = map[string]any{"token": HarborpulseBasinPump}
				case strings.Contains(req.Query, "calculatedMetricBackfillAvailability"):
					data["calculatedMetricBackfillAvailability"] = map[string]any{"available": true}
				case strings.Contains(req.Query, "startCalculatedMetricBackfill("):
					_ = json.Unmarshal(req.Variables["input"], &job)
					jobs++
					data["startCalculatedMetricBackfill"] = map[string]any{"token": "job"}
				case strings.Contains(req.Query, "createCommand("):
					var c struct{ Payload, DeviceToken, Name, Token string }
					_ = json.Unmarshal(req.Variables["request"], &c)
					if c.DeviceToken != HarborpulseSamplingGauge || c.Name != HarborpulseSamplingCommand || c.Payload != `{"seconds":15}` || c.Token == "" {
						t.Errorf("wrong command %+v", c)
					}
					commands++
					data["createCommand"] = map[string]any{"command": map[string]any{"token": c.Token}}
				default:
					var vars map[string]any
					raw, _ := json.Marshal(req.Variables)
					_ = json.Unmarshal(raw, &vars)
					var status int
					data, status = auth.dispatch(req.Query, vars)
					if status != 0 || data == nil {
						t.Errorf("unexpected query %s", req.Query)
						w.WriteHeader(500)
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			rt := &Runtime{Tenant: "acme", HTTPClient: server.Client(), Endpoints: Endpoints{DeviceMgmtGraphQL: server.URL, CommandMgmtGraphQL: server.URL, EventMgmtWS: server.URL}, Session: userclient.NewTenantSession(server.Client(), server.URL, "sim@example.invalid", "pw", "acme")}
			elapsed := func(h float64) time.Duration { return time.Duration((h - 6) / 24 * float64(s.dayLength)) }
			for _, h := range []float64{6, 12, 14, 15, 16, 16.5, 18.5, 19} {
				e := elapsed(h)
				if err := s.s3bBeats(context.Background(), rt, e, s.started.Add(e)); err != nil {
					t.Fatal(err)
				}
			}
			want := fmt.Sprint(HarborpulseNewThreshold)
			if defect == HarborpulseDefectAttributeNotUpdated {
				want = "1"
			}
			if attrs[HarborpulseAttributePump] != want {
				t.Fatalf("attribute %s want %s", attrs[HarborpulseAttributePump], want)
			}
			wantWrites := 4
			if defect == HarborpulseDefectAttributeNotUpdated {
				wantWrites = 3
			}
			if writes != wantWrites {
				t.Fatalf("%d writes, want %d (ensure must converge)", writes, wantWrites)
			}
			if commands != 1 || jobs != 1 || len(metadata) != 2 || metadata[0] != "{}" || metadata[1] != `{"well_depth_m":4}` {
				t.Fatalf("beats: commands %d jobs %d metadata %v", commands, jobs, metadata)
			}
			from, _ := time.Parse(time.RFC3339Nano, job["from"].(string))
			wantFrom := s.started.Add(elapsed(HarborpulseGapFrom)).Truncate(time.Microsecond)
			if (defect == HarborpulseDefectBackfillWrongWindow) == from.Equal(wantFrom) {
				t.Fatalf("job range %v, gap starts %s", job, wantFrom)
			}
			to, _ := time.Parse(time.RFC3339Nano, job["to"].(string))
			if from.Nanosecond()%1000 != 0 || to.Nanosecond()%1000 != 0 {
				t.Fatalf("submitted bounds have sub-microsecond precision: %v", job)
			}
			status := s.ScenarioClock(s.started.Add(elapsed(19)))
			if len(status.ThresholdChanges) != 1 || len(status.CalculatedBackfills) != 1 {
				t.Fatalf("missing beats %+v", status)
			}
			beat := status.CalculatedBackfills[0]
			if !beat.From.Equal(wantFrom) || beat.To.Nanosecond()%1000 != 0 {
				t.Fatalf("status bounds have wrong precision: %+v", beat)
			}
		})
	}
}

func TestHarborpulseTickEmitsWhenBackfillBeatFails(t *testing.T) {
	for _, failure := range []string{"unavailable", "start-error"} {
		t.Run(failure, func(t *testing.T) {
			s := NewHarborpulse(1, Load{}).(*harborpulse)
			s.started = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			now := s.started
			s.now = func() time.Time { return now }
			type event struct {
				Device, EventType string
				Payload           struct {
					Entries []struct {
						Measurements        map[string]string
						Latitude, Longitude string
					}
				}
			}
			var mu sync.Mutex
			var events []event
			attempts, starts, uplinks := 0, 0, 0
			auth := &fakeProfileServer{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case strings.HasSuffix(r.URL.Path, "/events"):
					var e event
					if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
						t.Error(err)
					}
					if attempts == 0 {
						t.Error("readings reached ingress before the backfill beat")
					}
					events = append(events, e)
					w.WriteHeader(http.StatusAccepted)
					return
				case strings.Contains(r.URL.Path, "/pending/"):
					_, _ = w.Write([]byte(`[]`))
					return
				case r.URL.Query().Get("event") == "up":
					uplinks++
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var req struct {
					Query     string
					Variables map[string]any
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				data := map[string]any{}
				switch {
				case strings.Contains(req.Query, "refresh("):
					data["refresh"] = map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
				case strings.Contains(req.Query, "entityAttributes("):
					data["entityAttributes"] = map[string]any{"results": []any{}}
				case strings.Contains(req.Query, "setEntityAttribute("):
					data["setEntityAttribute"] = map[string]any{}
				case strings.Contains(req.Query, "updateDevice("):
					data["updateDevice"] = map[string]any{"token": HarborpulseBasinPump}
				case strings.Contains(req.Query, "calculatedMetricBackfillAvailability"):
					attempts++
					data["calculatedMetricBackfillAvailability"] = map[string]any{"available": failure != "unavailable", "reason": "worker disabled"}
				case strings.Contains(req.Query, "startCalculatedMetricBackfill("):
					starts++
					_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "start refused"}}})
					return
				default:
					var status int
					data, status = auth.dispatch(req.Query, req.Variables)
					if status != 0 || data == nil {
						t.Errorf("unexpected query %s", req.Query)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			t.Setenv("DC_SIM_HARBORPULSE_SINK_URL", server.URL)
			rt := &Runtime{
				Tenant: "acme", InstanceId: "sim", HTTPClient: server.Client(),
				Devices: s.Manifest().Expand(1),
				Endpoints: Endpoints{Ingress: server.URL, DeviceMgmtGraphQL: server.URL, CommandMgmtGraphQL: server.URL,
					EventMgmtWS: server.URL, Integrations: server.URL + "/ingest"},
				Session:           userclient.NewTenantSession(server.Client(), server.URL, "sim@example.invalid", "pw", "acme"),
				IntegrationTokens: map[string]string{HarborpulseIntegration: "integration-token"},
			}
			for i, hour := range []float64{16, 18} {
				now = s.started.Add(time.Duration((hour - harborpulseStartHour) / 24 * float64(s.dayLength)))
				err := s.Tick(context.Background(), rt)
				wantError := "worker disabled"
				if failure == "start-error" {
					wantError = "start refused"
				}
				if err == nil || !strings.Contains(err.Error(), "harborpulse calculated-backfill:") || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("tick %d: %v, want the refused backfill beat", i, err)
				}
				func() {
					mu.Lock()
					defer mu.Unlock()
					wetWells := map[string]bool{}
					truckLocation := false
					wantFix := harborTruckFix(hour)
					for _, e := range events {
						for _, entry := range e.Payload.Entries {
							if e.EventType == "Measurement" && entry.Measurements[HarborpulseWetWellKey] != "" {
								wetWells[e.Device] = true
							}
							if e.Device == harborResponseTruck && e.EventType == "Location" &&
								entry.Latitude == formatFixValue(wantFix.Latitude) && entry.Longitude == formatFixValue(wantFix.Longitude) {
								truckLocation = true
							}
						}
					}
					if len(wetWells) != 3 || !truckLocation || uplinks != 4 {
						t.Errorf("tick %d: wet wells %v, truck location %v, uplinks %d; want all three pumps, truck at %.0fh and four uplinks", i, wetWells, truckLocation, uplinks, hour)
					}
					wantStarts := 0
					if failure == "start-error" {
						wantStarts = i + 1
					}
					if attempts != i+1 || starts != wantStarts {
						t.Errorf("tick %d: availability checks %d, start requests %d; want %d and %d (retry next tick)", i, attempts, starts, i+1, wantStarts)
					}
					events = nil
					uplinks = 0
				}()
			}
		})
	}
}

func TestHarborpulseSamplingEncoderAndClassAAnswer(t *testing.T) {
	encoder, err := payloadencode.Parse([]byte(HarborpulseSamplingEncoder))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encoder.Encode([]byte(`{"seconds":15}`))
	if err != nil {
		t.Fatal(err)
	}
	if encoded.FPort != 12 || !encoded.Confirmed || !bytes.Equal(encoded.Data, []byte{0, 15}) {
		t.Fatalf("encoding %+v", encoded)
	}
	for _, nack := range []bool{false, true} {
		t.Run(fmt.Sprint(nack), func(t *testing.T) {
			var reports []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer integration-token" {
					t.Error("report did not use integration credential")
				}
				var report map[string]any
				_ = json.NewDecoder(r.Body).Decode(&report)
				report["kind"] = r.URL.Query().Get("event")
				reports = append(reports, report)
				w.WriteHeader(202)
			}))
			defer server.Close()
			s := NewHarborpulse(1, Load{}).(*harborpulse)
			s.defects.downlinkNack = nack
			rt := &Runtime{Tenant: "acme", HTTPClient: server.Client(), Endpoints: Endpoints{Integrations: server.URL}, IntegrationTokens: map[string]string{HarborpulseIntegration: "integration-token"}}
			items := []lorawan.PendingDownlink{{ID: "queue-id", DevEUI: HarborpulseSamplingDevEUI, FPort: 12, Data: base64.StdEncoding.EncodeToString(encoded.Data), FCntDown: 7}}
			if err := s.answerGaugeQueue(context.Background(), rt, items, time.Minute, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := s.answerGaugeQueue(context.Background(), rt, items, time.Minute, time.Now()); err != nil {
				t.Fatal(err)
			}
			if len(reports) != 2 || reports[0]["kind"] != "txack" || reports[1]["kind"] != "ack" || reports[1]["acknowledged"] != !nack || reports[1]["queueItemId"] != "queue-id" || reports[1]["fCntDown"] != float64(7) {
				t.Fatalf("reports %+v", reports)
			}
			want := 15 * time.Second
			if nack {
				want = 0
			}
			if s.s3b.interval != want {
				t.Fatalf("sampling interval %s want %s", s.s3b.interval, want)
			}
		})
	}
}

func TestHarborpulseBackfillHasARealMissingConstantGap(t *testing.T) {
	plan, err := calcfield.NewPlan([]calcfield.Input{{Key: HarborpulseWetWellKey, Type: calcfield.Double}}, []calcfield.Definition{{Key: HarborpulseWellFillKey, Expression: harborpulseWellFillFormula, Type: calcfield.Double}})
	if err != nil {
		t.Fatal(err)
	}
	sample := plan.Env().Sample()
	mustSet(t, sample, HarborpulseWetWellKey, "2")
	if got := plan.Eval(sample, 0); got.OK() {
		t.Fatalf("missing depth calculated a value: %+v", got)
	}
	sample.UseConstants([]calcfield.Constant{calcfield.NewConstant(HarborpulseWellDepthConstant, 4)})
	if got := plan.Eval(sample, 0); !got.OK() || got.Value != "50" {
		t.Fatalf("restored depth: %+v", got)
	}
}

func TestHarborpulseS3bDocumentsUseServedSchemas(t *testing.T) {
	tests := []struct {
		area, doc string
		vars      map[string]any
	}{
		{"device-management", queryScenarioAttributes, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 10, "entityType": "device", "entity": HarborpulseBasinPump, "scope": "SHARED", "attrKeys": []string{HarborpulseWellAlarmAttr}}}},
		{"device-management", mutationScenarioAttribute, map[string]any{"request": map[string]any{"entityType": "device", "entity": HarborpulseBasinPump, "scope": "SHARED", "attrKey": HarborpulseWellAlarmAttr, "valueType": "DOUBLE", "value": "1.8"}}},
		{"command-delivery", mutationScenarioCommand, map[string]any{"request": map[string]any{"token": "hp-sampling", "deviceToken": HarborpulseSamplingGauge, "name": HarborpulseSamplingCommand, "payload": `{"seconds":15}`, "expiresAt": "2026-11-18T12:30:00Z"}}},
		{"event-management", queryScenarioBackfillAvailability, nil},
		{"event-management", mutationScenarioBackfill, map[string]any{"input": map[string]any{"profileToken": HarborpulsePumpProfile, "metricKeys": []string{HarborpulseWellFillKey}, "deviceTokens": []string{HarborpulseBasinPump}, "from": "2026-11-18T12:00:00Z", "to": "2026-11-18T12:01:00Z"}}},
	}
	for _, tt := range tests {
		if errs := servedSchema(t, tt.area).ValidateWithVariables(tt.doc, tt.vars); len(errs) > 0 {
			t.Errorf("%s: %v", tt.area, errs)
		}
	}
}

func TestHarborpulseNetworkServerOwnsGaugeCommands(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	integration, mqtt := 0, 0
	for _, d := range m.Expand(1) {
		if integrationFarEnd(d) {
			integration++
			if d.DeviceTypeToken != HarborpulseLoraGaugeType && d.DeviceTypeToken != HarborpulseLegacyGaugeType {
				t.Fatalf("unexpected integration device %s", d.Token)
			}
		} else {
			mqtt++
		}
	}
	if integration != 4 || mqtt != 33 {
		t.Fatalf("far ends: %d integration, %d MQTT", integration, mqtt)
	}
}

func TestHarborpulseAttributeRuleAndWaterModel(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	var when map[string]any
	for _, p := range m.Profiles {
		for _, r := range p.DetectionRules {
			if r.Token == HarborpulseAttributeRule {
				var rule struct{ When map[string]any }
				if err := json.Unmarshal([]byte(r.Definition), &rule); err != nil {
					t.Fatal(err)
				}
				when = rule.When
			}
		}
	}
	if when["thresholdAttr"] != HarborpulseWellAlarmAttr || when["threshold"] != nil {
		t.Fatalf("authored comparison %+v", when)
	}
	if len(m.NumericAttributes) != 3 {
		t.Fatalf("attributes %+v", m.NumericAttributes)
	}
	before, after := 0, 0
	for _, sample := range simulateHarborDay(t, true) {
		if sample.hour < 17 || sample.hour > 21.5 {
			continue
		}
		level := sample.values[HarborpulseAttributePump][HarborpulseWetWellKey]
		if level > HarborpulseOldThreshold(HarborpulseAttributePump)+0.03 && level < HarborpulseNewThreshold-0.03 {
			if sample.hour < HarborpulseAttributeChangeHour {
				before++
			} else {
				after++
			}
		}
	}
	if before < 3 || after < 3 {
		t.Fatalf("water model has only %d old/new-band readings before and %d after the change", before, after)
	}
}

func TestIntegrationGaugesNeverAttachToMQTT(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	driver := lc.sim.(*farEndSim)
	driver.devices = append(driver.devices, DeviceInstance{Token: "network-gauge", CredentialId: "gauge-credential", Aliases: []AliasSpec{{Scheme: AliasSchemeDevEUI, Value: HarborpulseSamplingDevEUI}}})
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, token := range fe.tokens() {
		if token == "network-gauge" {
			t.Fatal("integration gauge subscribed over MQTT")
		}
	}
}

func TestHarborpulseDownlinkWaitsForNextUplink(t *testing.T) {
	var sequence []string
	available, up := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pending/") {
			if strings.HasSuffix(r.URL.Path, HarborpulseSamplingDevEUI) {
				sequence = append(sequence, "poll")
				if available {
					available = false
					_ = json.NewEncoder(w).Encode([]lorawan.PendingDownlink{{ID: "during-uplink", DevEUI: HarborpulseSamplingDevEUI, FPort: 12, Data: "AA8="}})
					return
				}
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var report struct {
			DeviceInfo struct {
				DevEUI string `json:"devEui"`
			} `json:"deviceInfo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if report.DeviceInfo.DevEUI == HarborpulseSamplingDevEUI {
			kind := r.URL.Query().Get("event")
			sequence = append(sequence, kind)
			if kind == "up" {
				up++
				if up == 1 {
					available = true
				}
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("DC_SIM_HARBORPULSE_SINK_URL", server.URL)
	s := NewHarborpulse(1, Load{}).(*harborpulse)
	s.started = time.Now().UTC()
	rt := &Runtime{Tenant: "acme", HTTPClient: server.Client(), Endpoints: Endpoints{Integrations: server.URL + "/ingest"}, IntegrationTokens: map[string]string{HarborpulseIntegration: "token"}}
	model := s.ensureModel()
	tick := func(elapsed time.Duration) {
		if err := s.sendLoraUplinks(context.Background(), rt, model, elapsed, s.started.Add(elapsed)); err != nil {
			t.Fatal(err)
		}
	}
	tick(0)
	if strings.Join(sequence, ",") != "poll,up" {
		t.Fatalf("item queued during uplink was answered too soon: %v", sequence)
	}
	tick(5 * time.Second)
	if strings.Join(sequence, ",") != "poll,up,poll,up,txack,ack" {
		t.Fatalf("Class A report order %v", sequence)
	}
	tick(10 * time.Second)
	if up != 2 {
		t.Fatal("new sampling interval was not applied")
	}
	tick(20 * time.Second)
	if up != 3 {
		t.Fatal("gauge did not uplink at the new interval")
	}
}

func TestHarborpulseDownlinkRetriesAfterFailure(t *testing.T) {
	for _, failure := range []string{"uplink", "txack", "ack"} {
		t.Run(failure, func(t *testing.T) {
			ups, txacks, acks, attempts := 0, 0, 0, 0
			polled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/pending/") {
					if strings.HasSuffix(r.URL.Path, HarborpulseSamplingDevEUI) && !polled {
						polled = true
						item := lorawan.PendingDownlink{ID: "retry-item", DevEUI: HarborpulseSamplingDevEUI, FPort: 12, Data: "AA8="}
						// A duplicate in the fetched batch must not generate duplicate reports.
						_ = json.NewEncoder(w).Encode([]lorawan.PendingDownlink{item, item})
					} else {
						_, _ = w.Write([]byte(`[]`))
					}
					return
				}
				var body struct {
					DeviceInfo struct {
						DevEUI string `json:"devEui"`
					} `json:"deviceInfo"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.DeviceInfo.DevEUI != HarborpulseSamplingDevEUI {
					w.WriteHeader(202)
					return
				}
				kind := r.URL.Query().Get("event")
				if kind == "up" {
					ups++
				}
				target := failure
				if target == "uplink" {
					target = "up"
				}
				if kind == target {
					attempts++
					if attempts == 1 {
						w.WriteHeader(503)
						return
					}
				}
				switch kind {
				case "txack":
					txacks++
				case "ack":
					acks++
				}
				if kind != "up" && failure == "uplink" && ups < 2 {
					t.Error("reported before accepted uplink")
				}
				w.WriteHeader(202)
			}))
			defer server.Close()
			t.Setenv("DC_SIM_HARBORPULSE_SINK_URL", server.URL)
			s := NewHarborpulse(1, Load{}).(*harborpulse)
			s.started = time.Now().UTC()
			rt := &Runtime{Tenant: "acme", HTTPClient: server.Client(), Endpoints: Endpoints{Integrations: server.URL + "/ingest"}, IntegrationTokens: map[string]string{HarborpulseIntegration: "token"}}
			model := s.ensureModel()
			for i := 0; i < 3; i++ {
				elapsed := time.Duration(i) * 20 * time.Second
				err := s.sendLoraUplinks(context.Background(), rt, model, elapsed, s.started.Add(elapsed))
				if (i == 0) != (err != nil) {
					t.Fatalf("tick %d: %v", i, err)
				}
				if i == 0 && acks != 0 {
					t.Fatal("failed tick acknowledged item")
				}
				if i == 1 && (txacks != 1 || acks != 1) {
					t.Fatalf("retry: txacks=%d acks=%d", txacks, acks)
				}
			}
			wantAttempts := 2
			if failure == "uplink" {
				wantAttempts = 3
			}
			if txacks != 1 || acks != 1 || attempts != wantAttempts {
				t.Fatalf("txacks=%d acks=%d attempts=%d", txacks, acks, attempts)
			}
		})
	}
}
