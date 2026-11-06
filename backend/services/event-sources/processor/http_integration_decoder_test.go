// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
	"github.com/stretchr/testify/require"
)

// THE INTEGRATION ROUTE WITH A PAYLOAD DECODER BEHIND IT. Every refusal that exists only for a
// decoder is paired with the same request to a type that has none, which is accepted exactly as
// before, so a route that started refusing everything cannot pass.

type planFake struct {
	mu    sync.Mutex
	plan  *integration.DecoderPlan
	err   error
	calls int
}

func (f *planFake) Decoder(context.Context, string, string, string, string) (*integration.DecoderPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.plan == nil {
		return nil, f.err
	}
	p := *f.plan
	return &p, f.err
}

func (f *planFake) set(p *integration.DecoderPlan, err error) {
	f.mu.Lock()
	f.plan, f.err = p, err
	f.mu.Unlock()
}

// withDecoder gives the harness a decoder-plan backend and makes every device resolve with a type
// token, which is what the plan lookup is keyed on.
func (h *routeHarness) withDecoder(plan *integration.DecoderPlan, err error) *planFake {
	f := &planFake{plan: plan, err: err}
	h.route.Decoders = integration.NewDecoders(f, integration.DecoderConfig{Now: func() time.Time { return h.now }, OnUnsupported: func() { h.bump("probe_unsupported") }})
	h.route.Metrics.DecoderLookup = func(r string) { h.bump("lookup/" + r) }
	h.route.Metrics.DecoderCharge = func(n int) { h.mu.Lock(); h.metrics["charged"] += n; h.mu.Unlock() }
	h.backend.resolve = func(_, value string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Resolved, DeviceToken: "dev-" + strings.ToLower(value), DeviceTypeToken: "meter"}, nil
	}
	return f
}

func (h *routeHarness) chargedCounts() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.charged...)
}

func framePlan() *integration.DecoderPlan {
	return &integration.DecoderPlan{
		Present: true, Input: "FRAME", MaxReadings: 8, ProfileVersionToken: "meter@1",
		Cost: plancost.Shape{Composed: 100},
	}
}

func (h *routeHarness) linkMetricsOn() {
	h.backend.desc = func() integrationcodec.Descriptor {
		return integrationcodec.Descriptor{Type: integrationcodec.ChirpStackV4, Identity: "DEV_EUI", LinkMetrics: true}
	}
}

// ---------------------------------------------------------------------------------------------
// The carrier, and the charge (A12)
// ---------------------------------------------------------------------------------------------

func TestAnUplinkForATypeWithADecoderIsCarriedRawAndChargedForItsMaximum(t *testing.T) {
	h := newRouteHarness(t)
	h.linkMetricsOn()
	plan := framePlan()
	plan.Input = "DECODED"
	plan.Cost = plancost.Shape{Composed: 100, ParsesDecoded: true}
	h.withDecoder(plan, nil)

	rec := h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	p := h.published1()
	require.Equal(t, eventtype.IntegrationUplink, p.event.EventType)
	require.Equal(t, "integration:cs", p.event.Source)
	require.Equal(t, "cs", p.event.Integration, "the carrier is marked as the integration's, or the resolver refuses it")
	require.Equal(t, []byte("sealed|acme|cs|"+goodBearer), p.event.SealedIntegrationToken)
	require.Equal(t, "0101010101010101", p.event.IntegrationIdentifier)
	require.Equal(t, integrationDedupID("acme", "dev-0101010101010101", *p.event.AltId), p.dedupID)

	payload := p.payload.(*model.UnresolvedIntegrationUplinkPayload)
	require.Equal(t, []byte{0xaa}, payload.Frame)
	require.EqualValues(t, 1, *payload.FPort)
	require.EqualValues(t, 10, *payload.FCnt)
	require.Nil(t, payload.Json, "a network server's object is `decoded`, not `json`")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload.Decoded, &decoded))
	require.Equal(t, 21.5, decoded["temperature"], "nothing was flattened, so the object travels whole")
	require.Equal(t, map[string]float64{LinkGateways: 2, LinkRSSI: -36, LinkSNR: 10.5}, payload.Link)
	require.Equal(t, "meter@1", payload.ProfileVersionToken)

	// maxReadings 8 + 3 link readings = 11, charged once at the reading gate before the 202.
	require.EqualValues(t, 11, payload.MaxReadings, "the carrier says what the route charged")
	require.Equal(t, []int{11}, h.chargedCounts())
	require.Equal(t, 11, h.count("charged"))
	require.Equal(t, 1, h.count("lookup/present"))
	require.Equal(t, 1, h.count("uplink/published"))
}

// A12, the second half: a decoder that stores nothing still spends its cost.
func TestAnIgnoreOnlyDecoderWithAHighCostIsChargedItsCostNeverZero(t *testing.T) {
	h := newRouteHarness(t)
	h.withDecoder(&integration.DecoderPlan{
		Present: true, Input: "FRAME", MaxReadings: 0, ProfileVersionToken: "meter@1",
		Cost: plancost.Shape{Composed: 20_000},
	}, nil)
	require.Equal(t, http.StatusAccepted, h.up().Code)
	require.Equal(t, []int{10}, h.chargedCounts(), "ceil(20000 / 2000) readings, for a decoder that emits none")
	require.EqualValues(t, 10, h.published1().payload.(*model.UnresolvedIntegrationUplinkPayload).MaxReadings)

	// And one that is cheap and emits nothing is still charged the floor, which the gate does not
	// admit for free.
	h2 := newRouteHarness(t)
	h2.withDecoder(&integration.DecoderPlan{Present: true, Input: "FRAME", ProfileVersionToken: "meter@1", Cost: plancost.Shape{Composed: 5}}, nil)
	require.Equal(t, http.StatusAccepted, h2.up().Code)
	require.Equal(t, []int{1}, h2.chargedCounts())
}

func TestTheChargeFormulaTermByTerm(t *testing.T) {
	for _, tc := range []struct {
		name        string
		plan        integration.DecoderPlan
		link        bool
		jsonLen, dl int
		want        int
	}{
		{"floor of one", integration.DecoderPlan{}, false, 0, 0, 1},
		{"readings", integration.DecoderPlan{MaxReadings: 8}, false, 0, 0, 8},
		{"readings plus the link readings", integration.DecoderPlan{MaxReadings: 8}, true, 0, 0, 11},
		{"link alone", integration.DecoderPlan{}, true, 0, 0, 3},
		{"cost alone", integration.DecoderPlan{Cost: plancost.Shape{Composed: 20_000}}, false, 0, 0, 10},
		{"cost rounds up", integration.DecoderPlan{Cost: plancost.Shape{Composed: 2_001}}, false, 0, 0, 2},
		{"a max, not a sum", integration.DecoderPlan{MaxReadings: 8, Cost: plancost.Shape{Composed: 6_000}}, true, 0, 0, 11},
		{"cost beats readings", integration.DecoderPlan{MaxReadings: 2, Cost: plancost.Shape{Composed: 30_000}}, true, 0, 0, 15},
		{"a parse is priced by the size carried", integration.DecoderPlan{Cost: plancost.Shape{Composed: 0, ParsesJSON: true}}, false, 64 << 10, 0, 33},
		{"a small input is cheap", integration.DecoderPlan{Cost: plancost.Shape{Composed: 0, ParsesJSON: true}}, false, 200, 0, 1},
		{"a parse the decoder does not read is not charged", integration.DecoderPlan{Cost: plancost.Shape{Composed: 0}}, false, 64 << 10, 64 << 10, 1},
		{"both inputs", integration.DecoderPlan{Cost: plancost.Shape{ParsesJSON: true, ParsesDecoded: true}}, false, 64 << 10, 64 << 10, 66},
		{"never above one event's readings", integration.DecoderPlan{Cost: plancost.Shape{Composed: 10_000_000}}, false, 0, 0, 256},
	} {
		require.Equal(t, tc.want, DecoderCharge(tc.plan, tc.link, tc.jsonLen, tc.dl), tc.name)
	}
}

// A decoder that needs a frame is run for an envelope that carried none, so that the resolver
// records DECODE_FAILED/missing_input where the tenant can see it. It is never dropped here.
func TestAnUplinkWithoutTheInputTheDecoderNeedsIsStillPublished(t *testing.T) {
	h := newRouteHarness(t)
	h.withDecoder(framePlan(), nil)
	body := strings.Replace(fixtureBody(t, "chirpstack/up_no_object.json"), `"data": "qg==",`, ``, 1)
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)
	payload := h.published1().payload.(*model.UnresolvedIntegrationUplinkPayload)
	require.Nil(t, payload.Frame)
	require.Empty(t, h.undecodedRecorded(), "an uplink with a decoder is never `undecoded` at ingest")
	require.Zero(t, h.count("uplink/undecoded"))

	// And an envelope with no decoded object at all is published too.
	h2 := newRouteHarness(t)
	h2.withDecoder(framePlan(), nil)
	require.Equal(t, http.StatusAccepted, h2.post(csPath+upQuery, goodBearer, fixtureBody(t, "chirpstack/up_no_object.json")).Code)
	require.Equal(t, []byte{0xaa}, h2.published1().payload.(*model.UnresolvedIntegrationUplinkPayload).Frame)
}

func (h *routeHarness) undecodedRecorded() []string {
	// The first note of a tally is written by a goroutine of its own.
	time.Sleep(50 * time.Millisecond)
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	return append([]string(nil), h.backend.undecoded...)
}

func TestAGenericItemTravelsAsJsonWithoutItsIdentifier(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integrationcodec.Descriptor {
		g := `{"idFrom":"pointer:/id","timePointer":"/t"}`
		return integrationcodec.Descriptor{Type: integrationcodec.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	h.withDecoder(&integration.DecoderPlan{
		Present: true, Input: "JSON", MaxReadings: 2, ProfileVersionToken: "meter@1",
		Cost: plancost.Shape{Composed: 10, ParsesJSON: true},
	}, nil)
	require.Equal(t, http.StatusAccepted, h.post(csPath, goodBearer, `{"id":"p1","v":{"a":1.50},"t":"2022-07-18T09:34:00Z"}`).Code)
	payload := h.published1().payload.(*model.UnresolvedIntegrationUplinkPayload)
	require.Nil(t, payload.Decoded)
	require.NotContains(t, string(payload.Json), `"id"`, "the device identifier is about the message, not a reading")
	require.JSONEq(t, `{"v":{"a":1.50}}`, string(payload.Json), "the identifier and the time are about the message")
	require.Contains(t, string(payload.Json), "1.50", "numbers keep their written form")
	require.EqualValues(t, 2, payload.MaxReadings)
}

// ---------------------------------------------------------------------------------------------
// Strictness, and only with a decoder
// ---------------------------------------------------------------------------------------------

func deepObject(levels int) any {
	var v any = map[string]any{"leaf": 1}
	for i := 0; i < levels; i++ {
		v = map[string]any{"n": v}
	}
	return v
}

func (h *routeHarness) mutatedBody(fixture string, mutate func(map[string]any)) string {
	h.t.Helper()
	var m map[string]any
	require.NoError(h.t, json.Unmarshal([]byte(fixtureBody(h.t, fixture)), &m))
	mutate(m)
	b, err := json.Marshal(m)
	require.NoError(h.t, err)
	return string(b)
}

// The refusals that exist only for a type with a decoder. Each case is posted twice: with a
// decoder (refused, 400, counted, nothing published) and without one (accepted as it always was).
func TestStrictEncodingRefusesOnlyWhenADecoderIsPresent(t *testing.T) {
	type tc struct {
		name    string
		fixture string
		mutate  func(map[string]any)
		plan    *integration.DecoderPlan
		uplink  string
		// carried says that, with a decoder that does not read the broken input, the request is
		// accepted anyway.
		carried *integration.DecoderPlan
	}
	decodedPlan := &integration.DecoderPlan{Present: true, Input: "DECODED", MaxReadings: 1, ProfileVersionToken: "meter@1", Cost: plancost.Shape{Composed: 10, ParsesDecoded: true}}
	for _, c := range []tc{
		{
			name: "a frame that is not base64", fixture: "chirpstack/up_no_object.json",
			mutate:  func(m map[string]any) { m["data"] = "%%%not base64%%%" },
			plan:    framePlan(),
			uplink:  UplinkBadFrame,
			carried: decodedPlan,
		},
		{
			name: "an invalid port", fixture: "chirpstack/up_decoded.json",
			mutate: func(m map[string]any) { m["fPort"] = 300 },
			plan:   framePlan(),
			uplink: UplinkBadMeta,
		},
		{
			name: "an object nested past the cap", fixture: "chirpstack/up_decoded.json",
			mutate:  func(m map[string]any) { m["object"] = deepObject(40) },
			plan:    decodedPlan,
			uplink:  UplinkTooDeep,
			carried: framePlan(),
		},
		{
			name: "a frame over its cap", fixture: "chirpstack/up_no_object.json",
			mutate:  func(m map[string]any) { m["data"] = strings.Repeat("QUFB", 2000) },
			plan:    framePlan(),
			uplink:  UplinkTooLargeInput,
			carried: decodedPlan,
		},
	} {
		// With a decoder.
		h := newRouteHarness(t)
		h.withDecoder(c.plan, nil)
		rec := h.post(csPath+upQuery, goodBearer, h.mutatedBody(c.fixture, c.mutate))
		require.Equal(t, http.StatusBadRequest, rec.Code, c.name+": "+rec.Body.String())
		h.nothingPublished()
		require.Equal(t, 1, h.count("uplink/"+c.uplink), c.name)
		require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/bad_envelope"), c.name)
		require.Len(t, h.failed, 1, c.name+": a refused body is reported as a failed decode")
		require.Empty(t, h.chargedCounts(), c.name+": a refused uplink spent nothing")

		// The paired positive: the same body, no decoder, Cut A.
		h2 := newRouteHarness(t)
		h2.withDecoder(&integration.DecoderPlan{}, nil)
		rec = h2.post(csPath+upQuery, goodBearer, h2.mutatedBody(c.fixture, c.mutate))
		require.Equal(t, http.StatusAccepted, rec.Code, c.name+" without a decoder: "+rec.Body.String())
		require.Zero(t, h2.count("uplink/"+c.uplink), c.name)

		// A decoder that does not read the broken input does not refuse over it.
		if c.carried != nil {
			h3 := newRouteHarness(t)
			h3.withDecoder(c.carried, nil)
			rec = h3.post(csPath+upQuery, goodBearer, h3.mutatedBody(c.fixture, c.mutate))
			require.Equal(t, http.StatusAccepted, rec.Code, c.name+" for a decoder that does not read it: "+rec.Body.String())
			require.Equal(t, 1, h3.count("uplink/published"), c.name)
		}
	}
}

func TestAnObjectOverTheJsonCapIsRefusedForADecoderThatReadsIt(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integrationcodec.Descriptor {
		g := `{"idFrom":"pointer:/id","timePointer":"/t"}`
		return integrationcodec.Descriptor{Type: integrationcodec.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	h.withDecoder(&integration.DecoderPlan{Present: true, Input: "JSON", ProfileVersionToken: "meter@1", Cost: plancost.Shape{ParsesJSON: true}}, nil)
	big := fmt.Sprintf(`{"id":"p1","t":"2022-07-18T09:34:00Z","blob":"%s"}`, strings.Repeat("x", 65*1024))
	rec := h.post(csPath, goodBearer, big)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	h.nothingPublished()
	require.Zero(t, h.count("uplink/published"))
}

// ---------------------------------------------------------------------------------------------
// Cut A, response for response (the served interface does not move)
// ---------------------------------------------------------------------------------------------

type observed struct {
	status    int
	body      string
	contentTy string
	events    []string
	failed    []string
	charged   []int
	undecoded []string
	metrics   map[string]int
}

func snapshot(h *routeHarness, status int, body, contentType string) observed {
	h.mu.Lock()
	defer h.mu.Unlock()
	o := observed{status: status, body: body, contentTy: contentType, charged: append([]int(nil), h.charged...)}
	for _, p := range h.published {
		e := p.event
		o.events = append(o.events, fmt.Sprintf("%s|%s|%s|%d|%s|%v|%s|%s|%+v|%s", p.tenant, p.source, e.Device, e.EventType, e.Source,
			*e.AltId, e.Integration, e.IntegrationIdentifier, p.payload, p.dedupID))
	}
	for _, f := range h.failed {
		o.failed = append(o.failed, f.Error())
	}
	o.metrics = map[string]int{}
	for k, v := range h.metrics {
		if strings.HasPrefix(k, "request/") || strings.HasPrefix(k, "uplink/") {
			o.metrics[k] = v
		}
	}
	return o
}

// Every request below is posted to a route with no decoder support at all (the pre-Cut-B route),
// to one whose device-management answers "no decoder", and to one whose device-management predates
// the query. The three must be indistinguishable to the sender and to everything downstream: the
// status, the body, the headers, the published events, the failed-decode reports, the reading
// charges, the undecoded tally and the request and uplink counters.
func TestACutADeviceTypeIsServedResponseForResponseWhateverTheDecoderLookupSays(t *testing.T) {
	type request struct {
		name, path string
		fixture    string
		mutate     func(map[string]any)
		bearer     string
	}
	requests := []request{
		{name: "a decoded uplink", path: csPath + upQuery, fixture: "chirpstack/up_decoded.json", bearer: goodBearer},
		{name: "an uplink with no object", path: csPath + upQuery, fixture: "chirpstack/up_no_object.json", bearer: goodBearer},
		{name: "a bad frame", path: csPath + upQuery, fixture: "chirpstack/up_no_object.json", bearer: goodBearer,
			mutate: func(m map[string]any) { m["data"] = "%%%" }},
		{name: "a bad port", path: csPath + upQuery, fixture: "chirpstack/up_decoded.json", bearer: goodBearer,
			mutate: func(m map[string]any) { m["fPort"] = 300; m["fCnt"] = -4 }},
		{name: "a deep object", path: csPath + upQuery, fixture: "chirpstack/up_decoded.json", bearer: goodBearer,
			mutate: func(m map[string]any) { m["object"] = deepObject(40) }},
		{name: "a join", path: csPath + "?event=join", fixture: "chirpstack/join.json", bearer: goodBearer},
		{name: "a wrong token", path: csPath + upQuery, fixture: "chirpstack/up_decoded.json", bearer: "wrong"},
	}
	setups := map[string]func(h *routeHarness){
		"no decoder support": func(h *routeHarness) {
			h.backend.resolve = func(_, value string) (integration.Resolution, error) {
				return integration.Resolution{Outcome: integration.Resolved, DeviceToken: "dev-" + strings.ToLower(value), DeviceTypeToken: "meter"}, nil
			}
		},
		"a device-management that says no decoder":   func(h *routeHarness) { h.withDecoder(&integration.DecoderPlan{}, nil) },
		"a device-management that predates decoders": func(h *routeHarness) { h.withDecoder(nil, integration.ErrDecoderUnsupported) },
	}
	for _, r := range requests {
		results := map[string]observed{}
		for name, setup := range setups {
			h := newRouteHarness(t)
			h.linkMetricsOn()
			setup(h)
			body := fixtureBody(t, r.fixture)
			if r.mutate != nil {
				body = h.mutatedBody(r.fixture, r.mutate)
			}
			rec := h.post(r.path, r.bearer, body)
			o := snapshot(h, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
			o.undecoded = h.undecodedRecorded()
			results[name] = o
		}
		base := results["no decoder support"]
		for name, got := range results {
			require.Equal(t, base, got, "%s: %s differs from the route without decoder support", r.name, name)
		}
	}
}

// A14. A device-management that predates the query keeps being served as Cut A: accepted, and
// published as the measurement it always was. Not a 503.
func TestAnOldDeviceManagementKeepsServingCutA(t *testing.T) {
	h := newRouteHarness(t)
	f := h.withDecoder(nil, integration.ErrDecoderUnsupported)

	rec := h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	p := h.published1()
	require.Equal(t, eventtype.Measurement, p.event.EventType)
	require.Equal(t, "21.5", p.payload.(*model.UnresolvedMeasurementsPayload).Entries[0].Measurements["temperature"])
	require.Equal(t, 1, h.count("lookup/unsupported"))
	require.Equal(t, 1, h.count("probe_unsupported"))
	require.Zero(t, h.count("lookup/error"))
	require.Zero(t, h.count("request/CHIRPSTACK_V4/resolve_unavailable"))

	// Remembered: the next uplinks in the minute do not ask again.
	body := strings.ReplaceAll(fixtureBody(t, "chirpstack/up_decoded.json"), "3ac7e3c4-4401-4b8d-9386-a5c902f9202d", "4c8b1a2e-0000-4000-8000-00000000aaa1")
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	require.Equal(t, 1, calls)
}

// Not knowing whether a decoder exists is never flattening: anything but the one validation error
// is a 503, as every other failure to ask device-management is.
func TestAFailureToLearnWhetherThereIsADecoderIsA503(t *testing.T) {
	for name, err := range map[string]error{
		"unreachable":   errors.New("connection refused"),
		"a timeout":     context.DeadlineExceeded,
		"another error": errors.New("internal error"),
	} {
		h := newRouteHarness(t)
		h.withDecoder(nil, err)
		rec := h.up()
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
		h.nothingPublished()
		require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/resolve_unavailable"), name)
		require.Equal(t, 1, h.count("lookup/error"), name)
	}
}

func TestATokenDeviceManagementNoLongerAcceptsAtTheDecoderLookupIs401(t *testing.T) {
	h := newRouteHarness(t)
	h.withDecoder(nil, nil)
	rec := h.up()
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	h.nothingPublished()
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/unauthorized"))
}

// A device whose type is unknown to the lookup (a device-management that did not return a type
// token) is Cut A: there is nothing to ask the plan about.
func TestNoTypeTokenIsNoDecoderLookup(t *testing.T) {
	h := newRouteHarness(t)
	f := h.withDecoder(framePlan(), nil)
	h.backend.resolve = nil
	require.Equal(t, http.StatusAccepted, h.up().Code)
	require.Equal(t, eventtype.Measurement, h.published1().event.EventType)
	require.Zero(t, f.calls)
}

// ---------------------------------------------------------------------------------------------
// Skew (A25): what ES charged is what the carrier says
// ---------------------------------------------------------------------------------------------

func TestTheCarrierSaysWhatWasChargedAndOnWhichVersionSoDeviceManagementCanCountSkew(t *testing.T) {
	h := newRouteHarness(t)
	v1 := &integration.DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 1, ProfileVersionToken: "meter@1", Cost: plancost.Shape{Composed: 10}}
	f := h.withDecoder(v1, nil)
	require.Equal(t, http.StatusAccepted, h.up().Code)

	// The profile is published again with a decoder that emits four. This replica has not seen it.
	f.set(&integration.DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 4, ProfileVersionToken: "meter@2", Cost: plancost.Shape{Composed: 10}}, nil)
	body := strings.ReplaceAll(fixtureBody(t, "chirpstack/up_decoded.json"), "3ac7e3c4-4401-4b8d-9386-a5c902f9202d", "4c8b1a2e-0000-4000-8000-00000000aaa2")
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)

	// Inside the TTL it is still charged on v1, and says so: the resolver stores what v2 emits and
	// counts the excess against this charge.
	h.mu.Lock()
	second := h.published[1].payload.(*model.UnresolvedIntegrationUplinkPayload)
	h.mu.Unlock()
	require.EqualValues(t, 1, second.MaxReadings, "charged on the plan this replica holds")
	require.Equal(t, "meter@1", second.ProfileVersionToken)

	// Past the TTL the replica asks again and charges the new maximum.
	h.now = h.now.Add(integration.DefaultDecoderTTL + time.Second)
	body = strings.ReplaceAll(fixtureBody(t, "chirpstack/up_decoded.json"), "3ac7e3c4-4401-4b8d-9386-a5c902f9202d", "4c8b1a2e-0000-4000-8000-00000000aaa3")
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)
	h.mu.Lock()
	third := h.published[2].payload.(*model.UnresolvedIntegrationUplinkPayload)
	h.mu.Unlock()
	require.EqualValues(t, 4, third.MaxReadings)
	require.Equal(t, "meter@2", third.ProfileVersionToken)
	require.Equal(t, []int{1, 1, 4}, h.chargedCounts())
}

// A tenant over its reading ceiling is accepted and counted, never refused, and nothing is
// published: the same as for a flattened uplink.
func TestADecoderUplinkOverTheReadingCeilingIsShedNotRefused(t *testing.T) {
	h := newRouteHarness(t)
	h.withDecoder(framePlan(), nil)
	h.readingsOK.Store(false)
	require.Equal(t, http.StatusAccepted, h.up().Code)
	h.nothingPublished()
	require.Equal(t, 1, h.count("uplink/shed"))
	require.Zero(t, h.count("charged"), "a shed uplink is not counted as charged-and-carried")
}

func TestTheSameDecoderUplinkTwiceInOneRequestIsCarriedOnce(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integrationcodec.Descriptor {
		g := `{"idFrom":"pointer:/id","timePointer":"/t"}`
		return integrationcodec.Descriptor{Type: integrationcodec.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	h.withDecoder(&integration.DecoderPlan{Present: true, Input: "JSON", MaxReadings: 1, ProfileVersionToken: "p@1", Cost: plancost.Shape{ParsesJSON: true}}, nil)
	item := `{"id":"p1","v":1,"t":"2022-07-18T09:34:00Z"}`
	require.Equal(t, http.StatusAccepted, h.post(csPath, goodBearer, "["+item+","+item+"]").Code)
	h.published1()
	require.Equal(t, 1, h.count("uplink/duplicate_suppressed"))
}
