// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/lorawan"
)

const (
	HarborpulseDefectAttributeNotUpdated = "attribute-not-updated"
	HarborpulseDefectDownlinkNack        = "downlink-nack"
	HarborpulseDefectBackfillWrongWindow = "backfill-wrong-window"
	HarborpulseWellAlarmAttr             = "well_alarm_m"
	HarborpulseAttributeRule             = "hp-attribute-well"
	HarborpulseAttributeAlarm            = "attribute-well-high"
	HarborpulseAttributePump             = HarborpulseBasinPump
	HarborpulseAttributeChangeHour       = 18.5
	HarborpulseNewThreshold              = 1.8
	HarborpulseSamplingCommand           = "sampling-interval"
	HarborpulseSamplingGauge             = "hp-lora-gauge-north"
	HarborpulseSamplingDevEUI            = "70B3D57ED0A10001"
	HarborpulseSamplingPort              = 12
	HarborpulseSamplingSeconds           = 15
	HarborpulseSamplingSchema            = `[{"name":"seconds","dataType":"INT","required":true,"minValue":5,"maxValue":60}]`
	HarborpulseSamplingEncoder           = `{"fPort":12,"confirmed":true,"fields":[{"param":"seconds","type":"u16be"}]}`
	HarborpulseDownlinkConnector         = "hp-chirpstack-downlink"
	HarborpulseGapFrom                   = 14.0
	HarborpulseGapTo                     = 14.5
)

// AttributeSpec is a device's numeric SHARED attribute. Provision converges its natural key.
type AttributeSpec struct {
	Device, Key string
	Value       float64
}

type ThresholdChange struct {
	Day    int       `json:"day"`
	Device string    `json:"device"`
	At     time.Time `json:"at"`
	Old    float64   `json:"old"`
	New    float64   `json:"new"`
}

type CalculatedBackfillBeat struct {
	Day   int       `json:"day"`
	Token string    `json:"token"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

type harborS3bState struct {
	Changes    []ThresholdChange
	Jobs       []CalculatedBackfillBeat
	done       map[string]bool
	seen       map[string]bool
	pending    map[string][]lorawan.PendingDownlink
	reported   map[string]int
	interval   time.Duration
	nextUplink time.Duration
}

func HarborpulseOldThreshold(device string) float64 {
	switch device {
	case HarborpulseBasinPump:
		return 1.0
	case HarborpulseAirLockPump:
		return 1.3
	default:
		return 1.1
	}
}
func harborAttributes() []AttributeSpec {
	var out []AttributeSpec
	for _, d := range HarborpulseFleet() {
		if d.DeviceType == HarborpulsePumpType {
			out = append(out, AttributeSpec{d.Token, HarborpulseWellAlarmAttr, HarborpulseOldThreshold(d.Token)})
		}
	}
	return out
}

const queryScenarioAttributes = `query($c:EntityAttributeSearchCriteria!){entityAttributes(criteria:$c){results{attrKey value valueType} pagination{totalRecords}}}`
const mutationScenarioAttribute = `mutation($request:EntityAttributeSetRequest!){setEntityAttribute(request:$request){attrKey value}}`

func ensureAttribute(ctx context.Context, rt *Runtime, a AttributeSpec) error {
	var got struct {
		EntityAttributes struct {
			Results []struct{ Value, ValueType string }
		}
	}
	c := map[string]any{"pageNumber": 1, "pageSize": 10, "entityType": "device", "entity": a.Device, "scope": "SHARED", "attrKeys": []string{a.Key}}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryScenarioAttributes, map[string]any{"c": c}, &got); err != nil {
		return err
	}
	value := fmt.Sprint(a.Value)
	if len(got.EntityAttributes.Results) == 1 && got.EntityAttributes.Results[0].Value == value && got.EntityAttributes.Results[0].ValueType == "DOUBLE" {
		return nil
	}
	req := map[string]any{"entityType": "device", "entity": a.Device, "scope": "SHARED", "attrKey": a.Key, "valueType": "DOUBLE", "value": value}
	var out json.RawMessage
	return rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationScenarioAttribute, map[string]any{"request": req}, &out)
}

// The credential is a fixture credential, isolated by tenant, never a production key.
func HarborpulseQueueKey(tenant string) string { return "harborpulse-queue-" + tenant }
func harborDownlinkConnector() ConnectorSpec {
	cfg, _ := json.Marshal(map[string]any{"networkServer": "chirpstack_v4", "integration": HarborpulseIntegration,
		"baseUrl": fmt.Sprintf("http://%s:%d/chirpstack/%s", HarborpulseSinkHost, HarborpulseSinkHTTPPort, TenantPlaceholder)})
	return ConnectorSpec{Token: HarborpulseDownlinkConnector, Name: "Harbour ChirpStack queue", Type: "lorawan_downlink", Config: string(cfg), Secret: HarborpulseQueueKey(TenantPlaceholder)}
}

const mutationScenarioCommand = `mutation($request:CommandCreateRequest!){createCommand(request:$request){command{token} rejection{code}}}`
const queryScenarioBackfillAvailability = `query{calculatedMetricBackfillAvailability{available reason}}`
const mutationScenarioBackfill = `mutation($input:CalculatedMetricBackfillInput!){startCalculatedMetricBackfill(input:$input){token}}`

func scenarioEventsURL(rt *Runtime) string {
	u := rt.Endpoints.EventMgmtWS
	u = strings.Replace(u, "wss://", "https://", 1)
	return strings.Replace(u, "ws://", "http://", 1)
}

// s3bBeats runs each write once per day, retrying a refused/failed write on a later tick.
// Commands use the served idempotency key; a transport retry cannot enqueue a second command.
func (s *harborpulse) s3bBeats(ctx context.Context, rt *Runtime, elapsed time.Duration, now time.Time) error {
	day, hour := harborDayOf(elapsed, s.dayLength), harborHourOf(elapsed, s.dayLength)
	run := func(name string, from float64, fn func() error) error {
		if hour < from {
			return nil
		}
		key := fmt.Sprintf("%d/%s", day, name)
		s.mu.Lock()
		done := s.s3b.done[key]
		s.mu.Unlock()
		if done {
			return nil
		}
		if err := fn(); err != nil {
			return fmt.Errorf("harborpulse %s: %w", name, err)
		}
		s.mu.Lock()
		if s.s3b.done == nil {
			s.s3b.done = map[string]bool{}
		}
		s.s3b.done[key] = true
		s.mu.Unlock()
		return nil
	}
	// Restore the old shared thresholds at the beginning of every day, including a looping run.
	if err := run("sampling-reset", 6, func() error { s.mu.Lock(); s.s3b.interval = 0; s.s3b.nextUplink = 0; s.mu.Unlock(); return nil }); err != nil {
		return err
	}
	if err := run("threshold-reset", 6, func() error {
		for _, a := range harborAttributes() {
			if err := ensureAttribute(ctx, rt, a); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := run("depth-withheld", 12, func() error { return s.setBasinDepth(ctx, rt, false) }); err != nil {
		return err
	}
	if err := run("depth-restored", 15, func() error { return s.setBasinDepth(ctx, rt, true) }); err != nil {
		return err
	}
	if err := run("calculated-backfill", 16, func() error {
		var available struct {
			CalculatedMetricBackfillAvailability struct {
				Available bool
				Reason    string
			}
		}
		if err := rt.Session.Query(ctx, scenarioEventsURL(rt), queryScenarioBackfillAvailability, nil, &available); err != nil {
			return err
		}
		if !available.CalculatedMetricBackfillAvailability.Available {
			return fmt.Errorf("backfill unavailable: %s", available.CalculatedMetricBackfillAvailability.Reason)
		}
		at := func(h float64) time.Time {
			return s.startedAt().Add(time.Duration((float64(day)*24 + h - harborpulseStartHour) / 24 * float64(s.dayLength)))
		}
		from, to := at(HarborpulseGapFrom).Truncate(time.Microsecond), at(HarborpulseGapTo).Truncate(time.Microsecond)
		windowFrom, windowTo := from, to
		if s.defects.backfillWrongWindow {
			windowFrom = from.Add(-2 * time.Minute)
			windowTo = from.Add(-time.Minute)
		}
		input := map[string]any{"profileToken": HarborpulsePumpProfile, "metricKeys": []string{HarborpulseWellFillKey}, "deviceTokens": []string{HarborpulseBasinPump}, "from": windowFrom.Format(time.RFC3339Nano), "to": windowTo.Format(time.RFC3339Nano)}
		var out struct{ StartCalculatedMetricBackfill struct{ Token string } }
		if err := rt.Session.Query(ctx, scenarioEventsURL(rt), mutationScenarioBackfill, map[string]any{"input": input}, &out); err != nil {
			return err
		}
		if out.StartCalculatedMetricBackfill.Token == "" {
			return fmt.Errorf("backfill returned no token")
		}
		s.mu.Lock()
		s.s3b.Jobs = append(s.s3b.Jobs, CalculatedBackfillBeat{day, out.StartCalculatedMetricBackfill.Token, from, to})
		s.mu.Unlock()
		return nil
	}); err != nil {
		return err
	}
	if err := run("sampling-command", 16.5, func() error {
		req := map[string]any{"token": fmt.Sprintf("hp-sampling-%d-%d", s.startedAt().Unix(), day), "deviceToken": HarborpulseSamplingGauge, "name": HarborpulseSamplingCommand, "payload": `{"seconds":15}`, "expiresAt": now.Add(5 * time.Minute).Format(time.RFC3339)}
		var out struct {
			CreateCommand struct {
				Command   *struct{ Token string }
				Rejection *struct{ Code string }
			}
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.CommandMgmtGraphQL, mutationScenarioCommand, map[string]any{"request": req}, &out); err != nil {
			return err
		}
		if out.CreateCommand.Command == nil {
			return fmt.Errorf("sampling command refused: %+v", out.CreateCommand.Rejection)
		}
		return nil
	}); err != nil {
		return err
	}
	return run("threshold-change", HarborpulseAttributeChangeHour, func() error {
		if !s.defects.attributeNotUpdated {
			if err := ensureAttribute(ctx, rt, AttributeSpec{HarborpulseAttributePump, HarborpulseWellAlarmAttr, HarborpulseNewThreshold}); err != nil {
				return err
			}
		}
		// This is the intended beat, including under the skip-update control. Platform readings and
		// alarms must establish that the write took effect; this ledger is never success evidence.
		s.mu.Lock()
		s.s3b.Changes = append(s.s3b.Changes, ThresholdChange{day, HarborpulseAttributePump, now, HarborpulseOldThreshold(HarborpulseAttributePump), HarborpulseNewThreshold})
		s.mu.Unlock()
		return nil
	})
}

func (s *harborpulse) setBasinDepth(ctx context.Context, rt *Runtime, present bool) error {
	metadata := "{}"
	if present {
		depth := HarborpulseWellDepthM
		metadata = constantsMetadata(map[string]float64{HarborpulseWellDepthConstant: depth})
	}
	var out json.RawMessage
	return rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationUpdateDevice, map[string]any{"token": HarborpulseBasinPump, "request": map[string]any{"metadata": metadata}}, &out)
}

// The rig runs the sim on the host and forwards the sink's HTTP service to loopback.
func harborSinkURL() string {
	if u := os.Getenv("DC_SIM_HARBORPULSE_SINK_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return fmt.Sprintf("http://%s:%d", HarborpulseSinkHost, HarborpulseSinkHTTPPort)
}
