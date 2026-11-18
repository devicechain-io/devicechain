// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// harborProfileMetrics is a profile's measured metrics as the decoder compiler sees them.
type harborProfileMetrics map[string]calcfield.Type

func (m harborProfileMetrics) MetricType(key string) (calcfield.Type, bool) {
	t, ok := m[key]
	return t, ok
}

func (m harborProfileMetrics) ValidateMeasurement(key, value string) error {
	if _, ok := m[key]; !ok {
		return fmt.Errorf("undeclared %s", key)
	}
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return err
	}
	return nil
}

func harborProfile(t *testing.T, m SimManifest, token string) ProfileSpec {
	t.Helper()
	for _, p := range m.Profiles {
		if p.Token == token {
			return p
		}
	}
	t.Fatalf("no profile %s", token)
	return ProfileSpec{}
}

// harborDecoderPlan compiles a profile's decoder with the PLATFORM's compiler against the profile's
// own measured metrics, and runs its saved vectors, which gate the profile's publish.
func harborDecoderPlan(t *testing.T, p ProfileSpec) *payloaddecode.Plan {
	t.Helper()
	metrics := harborProfileMetrics{}
	for _, mx := range p.Metrics {
		ty, ok := calcfield.ParseType(mx.DataType)
		if !ok {
			t.Fatalf("%s: data type %q", mx.Key, mx.DataType)
		}
		metrics[mx.Key] = ty
	}
	doc, err := payloaddecode.ParseDocument([]byte(p.Decoder))
	if err != nil {
		t.Fatalf("%s: parse decoder: %v", p.Token, err)
	}
	plan, err := payloaddecode.Compile(doc, metrics)
	if err != nil {
		t.Fatalf("%s: the platform's compiler refuses the decoder: %v", p.Token, err)
	}
	for _, v := range plan.RunVectors() {
		if !v.OK {
			t.Fatalf("%s: vector %q fails, so the profile would not publish: %s", p.Token, v.Name, v.Error)
		}
	}
	return plan
}

func decodeLevel(plan *payloaddecode.Plan, frame []byte) (float64, *payloaddecode.DecodeError) {
	res, derr := plan.Decode(payloaddecode.Input{Frame: frame, LoRa: &payloaddecode.LoRa{FPort: HarborpulseLoraFPort, FCnt: 1}})
	if derr != nil {
		return 0, derr
	}
	for _, r := range res.Readings {
		if r.Key == HarborpulseWaterLevelKey {
			v, _ := strconv.ParseFloat(r.Value, 64)
			return v, nil
		}
	}
	return math.NaN(), nil
}

// The current decoder reads both frame layouts back to exactly the level the verifier expects; the
// legacy decoder reads layout 1 and FAILS layout 2 with an evaluation error (which the platform
// records as DECODE_FAILED); the legacy-decoder-fixed defect and the lora-wrong-scale defect break
// what they are named for.
func TestHarborpulseLoraDecodersDecodeAndFailAsDesigned(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	good := harborDecoderPlan(t, harborProfile(t, m, HarborpulseLoraGaugeProfile))
	legacy := harborDecoderPlan(t, harborProfile(t, m, HarborpulseLegacyGaugeProfile))
	devices := map[string]harborDevice{}
	for _, d := range harborLayout() {
		devices[d.token] = d
	}
	gauges := append(HarborpulseLoraGauges(), HarborpulseLegacyGauge)
	if len(gauges) != 4 {
		t.Fatalf("LoRaWAN gauges %v, want three current and one legacy", gauges)
	}
	var legacyOK, legacyFailed int
	for step := 0; step < 300; step++ {
		elapsed := time.Duration(step) * 4700 * time.Millisecond
		for _, tok := range gauges {
			want, ok := HarborpulseLoraLevelAt(1, tok, elapsed, harborpulseDayLength)
			if !ok {
				t.Fatalf("%s has no expected level", tok)
			}
			frame := harborLoraFrame(1, devices[tok], step, elapsed, harborpulseDayLength, false)
			plan := good
			if tok == HarborpulseLegacyGauge {
				plan = legacy
			}
			got, derr := decodeLevel(plan, frame)
			switch {
			case tok == HarborpulseLegacyGauge && frame[0] == 2:
				if derr == nil || derr.Reason != payloaddecode.ReasonEvalError {
					t.Fatalf("the legacy decoder read a layout-2 frame (%x): %v %v", frame, got, derr)
				}
				legacyFailed++
			case derr != nil:
				t.Fatalf("%s frame %x: %v", tok, frame, derr)
			case math.Abs(got-want) > 1e-9:
				t.Fatalf("%s frame %x decoded to %v, want %v", tok, frame, got, want)
			default:
				if tok == HarborpulseLegacyGauge {
					legacyOK++
				}
			}
			// The scale defect is visible in the decoded level.
			if tok != HarborpulseLegacyGauge {
				wrong, derr := decodeLevel(good, harborLoraFrame(1, devices[tok], step, elapsed, harborpulseDayLength, true))
				if derr != nil || (math.Abs(want) > 0.05 && math.Abs(wrong-want) < 0.5) {
					t.Fatalf("%s: the lora-wrong-scale frame decoded to %v (want far from %v): %v", tok, wrong, want, derr)
				}
			}
		}
	}
	if legacyOK == 0 || legacyFailed == 0 {
		t.Fatalf("the legacy gauge decoded %d and failed %d; it must do both", legacyOK, legacyFailed)
	}
	// Fixed: the legacy profile carries the current decoder and fails nothing.
	fixed := NewHarborpulse(1, Load{})
	if err := fixed.(ScenarioConfigurable).ConfigureScenario(ScenarioOptions{Defects: []string{HarborpulseDefectLegacyDecoderFixed}}); err != nil {
		t.Fatal(err)
	}
	if harborProfile(t, fixed.Manifest(), HarborpulseLegacyGaugeProfile).Decoder != HarborpulseLoraDecoder {
		t.Fatal("legacy-decoder-fixed did not give the legacy gauge the current decoder")
	}
}

// The gauges send uplinks, never an HTTP reading; their devices carry a DevEUI the integration is
// scoped to resolve.
func TestHarborpulseLoraGaugesAreIntegrationDevices(t *testing.T) {
	h := NewHarborpulse(1, Load{}).(*harborpulse)
	m := h.Manifest()
	model := newHarborModel(1)
	for _, d := range m.Expand(1) {
		lora := d.DeviceTypeToken == HarborpulseLoraGaugeType || d.DeviceTypeToken == HarborpulseLegacyGaugeType
		if lora != (len(d.Aliases) == 1) {
			t.Errorf("%s (%s) aliases %v", d.Token, d.DeviceTypeToken, d.Aliases)
		}
		if lora && model.reading(d.Token, time.Minute, time.Now()) != nil {
			t.Errorf("%s publishes an HTTP reading; it is behind the network server", d.Token)
		}
	}
	if len(m.Integrations) != 1 || len(m.Integrations[0].DeviceTypeTokens) != 2 {
		t.Fatalf("integrations %+v", m.Integrations)
	}
	ups := model.loraUplinks(time.Minute)
	if len(ups) != 4 || ups[0].n != 0 || model.loraUplinks(time.Minute)[0].n != 1 {
		t.Fatalf("uplinks %+v; want one per gauge with a counter that advances", ups)
	}
}

// The well fill formula compiles under the platform's own compiler and computes level over depth.
func TestHarborpulseWellFillIsCalculatedFromTheWellDepth(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	p := harborProfile(t, m, HarborpulsePumpProfile)
	var measured []calcfield.Input
	var defs []calcfield.Definition
	for _, mx := range p.Metrics {
		ty, _ := calcfield.ParseType(mx.DataType)
		if mx.Expression == "" {
			measured = append(measured, calcfield.Input{Key: mx.Key, Type: ty})
		} else {
			defs = append(defs, calcfield.Definition{Key: mx.Key, Expression: mx.Expression, Type: ty})
		}
	}
	if len(defs) != 1 || defs[0].Key != HarborpulseWellFillKey {
		t.Fatalf("calculated metrics %+v, want %s alone", defs, HarborpulseWellFillKey)
	}
	plan, err := calcfield.NewPlan(measured, defs)
	if err != nil || len(plan.Problems()) > 0 {
		t.Fatalf("the platform's compiler refuses the formula: %v %v", err, plan.Problems())
	}
	s := plan.Env().Sample()
	for k, v := range map[string]string{HarborpulseWetWellKey: "2", HarborpulsePumpRunningKey: "1", HarborpulsePumpCurrentKey: "17", HarborpulseFlowKey: "120"} {
		mustSet(t, s, k, v)
	}
	s.UseConstants([]calcfield.Constant{calcfield.NewConstant(HarborpulseWellDepthConstant, HarborpulseWellDepthM)})
	if got := plan.Eval(s, 0); !got.OK() || got.Value != "50" {
		t.Fatalf("2 m in a 4 m well = %+v, want 50", got)
	}
	// Every pump carries the depth; the wrong-well-depth defect carries another.
	depths := func(defects ...string) map[float64]int {
		h := NewHarborpulse(1, Load{})
		if err := h.(ScenarioConfigurable).ConfigureScenario(ScenarioOptions{Defects: defects}); err != nil {
			t.Fatal(err)
		}
		out := map[float64]int{}
		for _, d := range h.Manifest().Expand(1) {
			if d.DeviceTypeToken != HarborpulsePumpType {
				continue
			}
			var c map[string]float64
			if err := json.Unmarshal([]byte(d.Metadata), &c); err != nil {
				t.Fatalf("%s metadata %q", d.Token, d.Metadata)
			}
			out[c[HarborpulseWellDepthConstant]]++
		}
		return out
	}
	if got := depths(); got[HarborpulseWellDepthM] != 3 || len(got) != 1 {
		t.Fatalf("pump depths %v, want 3 at %v", got, HarborpulseWellDepthM)
	}
	if got := depths(HarborpulseDefectWrongWellDepth); got[HarborpulseWellDepthM] != 0 {
		t.Fatalf("wrong-well-depth left depths %v", got)
	}
}

// The truck is inside the flood plain exactly over [HarborpulseTruckInPlainFrom,
// HarborpulseTruckInPlainTo), which is what the fence rule fires on, and never under truck-stays-home.
func TestHarborpulseTruckFenceWindow(t *testing.T) {
	var ring [][2]float64
	for _, g := range harborpulseGeoFences() {
		if g.Token == HarborpulseFloodPlainFence {
			ring = g.Ring
		}
	}
	inside := func(f Fix) bool {
		minLon, maxLon, minLat, maxLat := ring[0][0], ring[0][0], ring[0][1], ring[0][1]
		for _, p := range ring {
			minLon, maxLon = min(minLon, p[0]), max(maxLon, p[0])
			minLat, maxLat = min(minLat, p[1]), max(maxLat, p[1])
		}
		return f.Longitude >= minLon && f.Longitude <= maxLon && f.Latitude >= minLat && f.Latitude <= maxLat
	}
	home := newHarborModelWith(1, harborpulseDayLength, harborDefects{truckStaysHome: true})
	for h := 0.0; h < 24; h += 0.01 {
		want := h >= HarborpulseTruckInPlainFrom+0.005 && h < HarborpulseTruckInPlainTo-0.005
		edge := math.Abs(h-HarborpulseTruckInPlainFrom) < 0.01 || math.Abs(h-HarborpulseTruckInPlainTo) < 0.01
		if got := inside(harborTruckFix(h)); got != want && !edge {
			t.Fatalf("at %.2fh inside=%v, want %v", h, got, want)
		}
		if inside(home.truckFix(h)) {
			t.Fatalf("truck-stays-home: in the flood plain at %.2fh", h)
		}
	}
	var fence DetectionRuleSpec
	for _, r := range harborProfile(t, NewHarborpulse(1, Load{}).Manifest(), HarborpulseTruckProfile).DetectionRules {
		if r.Token == HarborpulseTruckFenceRule {
			fence = r
		}
	}
	if !fence.ReadsNoMetric || fence.Metric != "" || !strings.Contains(fence.Definition, `geo.inFence(\"`+HarborpulseFloodPlainFence+`\")`) {
		t.Fatalf("fence rule %+v", fence)
	}
}

// The delivery rules name the sink, the signing handle and the connector the manifest provisions,
// and the defects change exactly that.
func TestHarborpulseDeliveriesAndTheirDefects(t *testing.T) {
	manifest := func(defects ...string) SimManifest {
		h := NewHarborpulse(1, Load{})
		if err := h.(ScenarioConfigurable).ConfigureScenario(ScenarioOptions{Defects: defects}); err != nil {
			t.Fatal(err)
		}
		m := h.Manifest()
		if err := m.Validate(); err != nil {
			t.Fatalf("%v: %v", defects, err)
		}
		return m
	}
	rule := func(m SimManifest, token string) string {
		for _, r := range harborProfile(t, m, HarborpulseGaugeProfile).DetectionRules {
			if r.Token == token {
				return r.Definition
			}
		}
		t.Fatalf("no rule %s", token)
		return ""
	}
	clean := manifest()
	hook := rule(clean, HarborpulseFloodWebhookRule)
	if !strings.Contains(hook, HarborpulseWebhookURL()) || !strings.Contains(hook, `"signingSecretRef":"`+HarborpulseSigningHandle+`"`) {
		t.Fatalf("webhook rule %s", hook)
	}
	if strings.Contains(rule(manifest(HarborpulseDefectUnsignedWebhook), HarborpulseFloodWebhookRule), "signingSecretRef") {
		t.Fatal("unsigned-webhook still signs")
	}
	if !strings.Contains(rule(clean, HarborpulseBulletinRule), `"connectorRef":"`+HarborpulseBulletinConnector+`"`) {
		t.Fatal("the bulletin rule does not publish through the bulletin connector")
	}
	if !strings.Contains(clean.Connectors[0].Config, fmt.Sprintf(":%d", HarborpulseSinkMQTTPort)) ||
		strings.Contains(manifest(HarborpulseDefectMQTTUnreachable).Connectors[0].Config, fmt.Sprintf(":%d", HarborpulseSinkMQTTPort)) {
		t.Fatal("mqtt-unreachable does not move the connector off the sink's broker port")
	}
	if got := HarborpulseBulletinTopic("t1"); got != "harborpulse/t1/flood-bulletin" || HarborpulseDutyRecipient("t1") != "duty-t1@port-alder.example" {
		t.Fatalf("tenant placeholders: %q %q", got, HarborpulseDutyRecipient("t1"))
	}
	// The rules' levels are the flood tiers the model is built against.
	var doc struct {
		When struct {
			Threshold float64 `json:"threshold"`
		} `json:"when"`
	}
	_ = json.Unmarshal([]byte(hook), &doc)
	if doc.When.Threshold != HarborpulseFloodCriticalM {
		t.Fatalf("webhook level %v, want the critical tier %v", doc.When.Threshold, HarborpulseFloodCriticalM)
	}
	for _, d := range HarborpulseDefects() {
		manifest(d)
	}
}

// A rule declaring it reads no metric is held to that, and a rule naming an unprovisioned
// destination or a device alias no integration resolves is refused.
func TestValidateHoldsTheS3Declarations(t *testing.T) {
	base := func() SimManifest { return NewHarborpulse(1, Load{}).Manifest() }
	truck := func(m *SimManifest) *ProfileSpec {
		for i := range m.Profiles {
			if m.Profiles[i].Token == HarborpulseTruckProfile {
				return &m.Profiles[i]
			}
		}
		t.Fatal("no truck profile")
		return nil
	}
	cases := []struct {
		name   string
		mutate func(*SimManifest)
		want   string
	}{
		{"fence rule that also names a metric", func(m *SimManifest) { truck(m).DetectionRules[0].Metric = HarborpulseSpeedKey }, "says it reads no metric but names"},
		{"no-metric rule whose definition reads one", func(m *SimManifest) {
			truck(m).DetectionRules[0].Definition = ThresholdAlarmRule{Token: "x", Name: "x", Metric: HarborpulseSpeedKey, Op: OpGt, Threshold: 1,
				Severity: SeverityWarning, AlarmKey: "x", Enabled: true}.DefinitionJSON()
		}, "but its definition reads"},
		{"fence rule that does not declare it", func(m *SimManifest) { truck(m).DetectionRules[0].ReadsNoMetric = false }, "names no metric"},
		{"unprovisioned signing handle", func(m *SimManifest) { m.WebhookSigningHandles = nil }, "does not provision"},
		{"unprovisioned connector", func(m *SimManifest) { m.Connectors = nil }, "does not provision"},
		{"alias no integration resolves", func(m *SimManifest) { m.Integrations[0].DeviceTypeTokens = []string{HarborpulseLoraGaugeType} }, "in no integration's scope"},
		{"policy paging an unknown channel", func(m *SimManifest) { m.NotificationChannels = nil }, "does not provision"},
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("the clean manifest: %v", err)
	}
	for _, c := range cases {
		m := base()
		c.mutate(&m)
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

// A decoder is profile-version content: changing it must change the content marker, or a fixed
// decoder would never be republished to a tenant that ran the broken one. A profile with none keeps
// the marker it had before decoders existed.
func TestTheDecoderIsInTheProfileMarker(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	p := harborProfile(t, m, HarborpulseLegacyGaugeProfile)
	fixed := p
	fixed.Decoder = HarborpulseLoraDecoder
	none := p
	none.Decoder = ""
	if profileContentMarker(p) == profileContentMarker(fixed) || profileContentMarker(p) == profileContentMarker(none) {
		t.Fatal("the decoder does not change the profile's content marker")
	}
	again := none
	again.Decoder = ""
	if profileContentMarker(none) != profileContentMarker(again) {
		t.Fatal("the marker is not stable")
	}
}
