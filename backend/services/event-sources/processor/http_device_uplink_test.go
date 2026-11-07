// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
	"github.com/stretchr/testify/require"
)

// A DEVICE'S OWN PAYLOAD. Every refusal is paired with the request that succeeds, so a route that
// refuses everything cannot pass, and the default /events route is checked beside it.

type devicePlanFake struct {
	mu    sync.Mutex
	plan  *integration.DecoderPlan
	err   error
	calls int
	// valid is the set of credential ids device-management authenticates; any other is refused.
	valid map[string]bool
}

func (f *devicePlanFake) DeviceDecoder(_ context.Context, _, _ string, cred integration.DeviceCredential) (*integration.DecoderPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if !f.valid[cred.ID] {
		return nil, nil
	}
	if f.plan == nil {
		return &integration.DecoderPlan{}, nil
	}
	p := *f.plan
	return &p, nil
}

type deviceHarness struct {
	es         *HttpEventSource
	plans      *devicePlanFake
	published  []*model.UnresolvedEvent
	payloads   []*model.UnresolvedIntegrationUplinkPayload
	pubErr     error
	results    []string
	charged    []int
	readings   []int
	allowed    bool
	allowCalls []string
	admitErr   error
	events     int
	failed     int
	received   int
}

func newDeviceHarness(t *testing.T, plan *integration.DecoderPlan) *deviceHarness {
	t.Helper()
	h := &deviceHarness{plans: &devicePlanFake{plan: plan, valid: map[string]bool{"tok-1": true, "u": true}}, allowed: true}
	es, err := NewHttpEventSource("http", map[string]string{}, "inst", config.HttpIngest{}, NewJsonDecoder(nil),
		func(string, []byte) { h.received++ },
		func(string, string, *model.UnresolvedEvent, interface{}, string) error { h.events++; return nil },
		func(string, string, []byte, error) error { h.failed++; return nil },
		func(_, tenant string, _ time.Time, _ bool, _ Origin) bool {
			h.allowCalls = append(h.allowCalls, tenant)
			return h.allowed
		},
		func(_, _ string, _ time.Time, _ bool, _ Origin, n int) bool {
			h.readings = append(h.readings, n)
			return true
		},
		func(string) error { return h.admitErr }, nil)
	require.NoError(t, err)
	rt := &DeviceUplinkRoute{
		Plans: integration.NewDeviceDecoders(h.plans, integration.DeviceDecoderConfig{}),
		Publish: func(_, _ string, e *model.UnresolvedEvent, p interface{}) error {
			if h.pubErr != nil {
				return h.pubErr
			}
			h.published = append(h.published, e)
			h.payloads = append(h.payloads, p.(*model.UnresolvedIntegrationUplinkPayload))
			return nil
		},
		Result:  func(r string) { h.results = append(h.results, r) },
		Charged: func(n int) { h.charged = append(h.charged, n) },
	}
	require.NoError(t, es.UseDeviceUplinks(rt))
	h.es = es
	return h
}

func (h *deviceHarness) post(path, ctype, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.es.handler().ServeHTTP(rec, req)
	return rec
}

const uplinkPath = "/inst/acme/devices/meter-1/uplink"

var bearer = map[string]string{"Authorization": "Bearer tok-1"}

func fplan() *integration.DecoderPlan {
	return &integration.DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 4, ProfileVersionToken: "meter@3", Cost: plancost.Shape{Composed: 100}}
}

func TestDeviceUplinkCarriesTheFrameAsAnIntegrationUplink(t *testing.T) {
	h := newDeviceHarness(t, fplan())
	rec := h.post(uplinkPath, "application/octet-stream", "\x01\x02\x03", bearer)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Len(t, h.published, 1)
	e, p := h.published[0], h.payloads[0]
	require.Equal(t, eventtype.IntegrationUplink, e.EventType)
	require.Equal(t, "meter-1", e.Device)
	require.Empty(t, e.Integration, "a device's own uplink carries no integration mark")
	require.Nil(t, e.SealedIntegrationToken)
	require.NotNil(t, e.CredentialType)
	require.Equal(t, "ACCESS_TOKEN", *e.CredentialType)
	require.Equal(t, "tok-1", *e.CredentialId)
	require.Equal(t, []byte{1, 2, 3}, p.Frame)
	require.Nil(t, p.Json)
	require.Equal(t, "meter@3", p.ProfileVersionToken)
	require.Equal(t, []int{4}, h.readings, "charged exactly the decoder charge")
	require.Equal(t, uint32(4), p.MaxReadings)
	require.Equal(t, []string{DeviceUplinkAccepted}, h.results)
}

func TestDeviceUplinkChargeEqualsDecoderCharge(t *testing.T) {
	plan := fplan()
	plan.Cost = plancost.Shape{Composed: 100_000} // cost term 50 beats maxReadings 4
	h := newDeviceHarness(t, plan)
	require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "", "x", bearer).Code)
	require.Equal(t, []int{DecoderCharge(*plan, false, 0, 0)}, h.readings)
	require.Equal(t, 50, h.readings[0])
}

func TestDeviceUplinkJSONBodyIsCarriedWhenTheDecoderReadsIt(t *testing.T) {
	plan := fplan()
	plan.Input, plan.Cost.ParsesJSON = "JSON", true
	h := newDeviceHarness(t, plan)
	rec := h.post(uplinkPath, "application/json; charset=utf-8", `{"t": 21.5}`, bearer)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"t":21.5}`, string(h.payloads[0].Json))

	// A body that is not JSON, or not an object, is a 400 for a decoder that reads JSON...
	require.Equal(t, http.StatusBadRequest, h.post(uplinkPath, "application/json", `not json`, bearer).Code)
	require.Equal(t, http.StatusBadRequest, h.post(uplinkPath, "application/json", `[1,2]`, bearer).Code)
	// ...and a non-JSON content type carries no json: the decoder records missing_input.
	h.published, h.payloads = nil, nil
	require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "application/octet-stream", `{"t":1}`, bearer).Code)
	require.Nil(t, h.payloads[0].Json)

	// A frame decoder does not parse a JSON body at all, so a malformed one is still accepted.
	f := newDeviceHarness(t, fplan())
	require.Equal(t, http.StatusAccepted, f.post(uplinkPath, "application/json", `not json`, bearer).Code)
	require.Equal(t, []byte(`not json`), f.payloads[0].Frame)
}

func TestDeviceUplinkTooDeepAndTooLarge(t *testing.T) {
	plan := fplan()
	plan.Input, plan.Cost.ParsesJSON = "JSON", true
	h := newDeviceHarness(t, plan)
	deep := strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40)
	require.Equal(t, http.StatusBadRequest, h.post(uplinkPath, "application/json", deep, bearer).Code)

	big := `{"k":"` + strings.Repeat("x", plancost.MaxJSONBytes) + `"}`
	require.Equal(t, http.StatusRequestEntityTooLarge, h.post(uplinkPath, "application/json", big, bearer).Code)
	require.Empty(t, h.published)

	// A frame decoder: a frame over the cap is 413; a small one is accepted.
	f := newDeviceHarness(t, fplan())
	require.Equal(t, http.StatusRequestEntityTooLarge, f.post(uplinkPath, "", strings.Repeat("x", plancost.MaxFrameBytes+1), bearer).Code)
	require.Equal(t, http.StatusAccepted, f.post(uplinkPath, "", strings.Repeat("x", plancost.MaxFrameBytes), bearer).Code)
	// The whole-body cap.
	require.Equal(t, http.StatusRequestEntityTooLarge, f.post(uplinkPath, "", strings.Repeat("x", maxBodyBytes+1), bearer).Code)
}

func TestDeviceUplinkRefusals(t *testing.T) {
	ok := func(h *deviceHarness) {
		require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "", "x", bearer).Code)
	}

	// No credential: 401, and nothing is looked up or published.
	h := newDeviceHarness(t, fplan())
	rec := h.post(uplinkPath, "", "x", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	require.Zero(t, h.plans.calls)
	require.Empty(t, h.published)
	ok(h)

	// Both forms of credential at once, a half-given trio, a bad scheme.
	for _, hdr := range []map[string]string{
		{"Authorization": "Bearer a", "X-DeviceChain-Credential-Type": "X"},
		{"X-DeviceChain-Credential-Type": "MQTT_BASIC"},
		{"Authorization": "Basic abc"},
		{"Authorization": "Bearer "},
	} {
		require.Equal(t, http.StatusBadRequest, h.post(uplinkPath, "", "x", hdr).Code, "%v", hdr)
	}
	// The explicit trio is carried as given.
	n := len(h.published)
	require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "", "x", map[string]string{
		"X-DeviceChain-Credential-Type": "MQTT_BASIC", "X-DeviceChain-Credential-Id": "u", "X-DeviceChain-Credential-Secret": "s"}).Code)
	e := h.published[n]
	require.Equal(t, "MQTT_BASIC", *e.CredentialType)
	require.Equal(t, "u", *e.CredentialId)
	require.Equal(t, "s", *e.CredentialSecret)

	// Bad tenant and device grammar.
	require.Equal(t, http.StatusBadRequest, h.post("/inst/ac%20me/devices/d/uplink", "", "x", bearer).Code)
	require.Equal(t, http.StatusBadRequest, h.post("/inst/acme/devices/d%20x/uplink", "", "x", bearer).Code)

	// No decoder (or no such device): 422, never accepted.
	nd := newDeviceHarness(t, nil)
	require.Equal(t, http.StatusUnprocessableEntity, nd.post(uplinkPath, "", "x", bearer).Code)
	require.Empty(t, nd.published)

	// Plan unavailable: a retryable 503, never "absent".
	pu := newDeviceHarness(t, fplan())
	pu.plans.err = errors.New("dm down")
	rec = pu.post(uplinkPath, "", "x", bearer)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	pu.plans.err = integration.ErrDeviceDecoderUnsupported
	rec = pu.post("/inst/acme/devices/other/uplink", "", "x", bearer)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	// Tenant over its message ceiling.
	rl := newDeviceHarness(t, fplan())
	rl.allowed = false
	rec = rl.post(uplinkPath, "", "x", bearer)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, 1, rl.plans.calls, "the credential is proven before the ceiling is spent")
	require.Zero(t, rl.received, "and the body is not read for a request the ceiling refused")

	// Backpressure and publish outcomes.
	bp := newDeviceHarness(t, fplan())
	bp.admitErr = messaging.ErrStreamBackpressure
	rec = bp.post(uplinkPath, "", "x", bearer)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	bp.admitErr = nil
	bp.pubErr = messaging.ErrStreamBackpressure
	require.NotEmpty(t, bp.post(uplinkPath, "", "x", bearer).Header().Get("Retry-After"))
	bp.pubErr = errors.New("timeout")
	rec = bp.post(uplinkPath, "", "x", bearer)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Empty(t, rec.Header().Get("Retry-After"), "a failed publish may have been stored")
	bp.pubErr = ErrEventNotBuilt
	require.Equal(t, http.StatusInternalServerError, bp.post(uplinkPath, "", "x", bearer).Code)
	bp.pubErr = nil
	ok(bp)
}

func TestDeviceUplinkRouteExistsOnlyWhenEnabled(t *testing.T) {
	es, err := NewHttpEventSource("http", map[string]string{}, "inst", config.HttpIngest{}, NewJsonDecoder(nil),
		func(string, []byte) {}, nil, nil, nil, func(_, _ string, _ time.Time, _ bool, _ Origin, _ int) bool { return true },
		func(string) error { return nil }, nil)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, uplinkPath, strings.NewReader("x"))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Error(t, es.UseDeviceUplinks(&DeviceUplinkRoute{}))
}

func TestEventsRouteIsUnchangedByTheDeviceRoute(t *testing.T) {
	h := newDeviceHarness(t, fplan())
	body := `{"device":"meter-1","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`
	rec := h.post("/inst/acme/events", "application/octet-stream", body, nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.events)
	require.Zero(t, h.plans.calls, "the default route never asks for a decoder plan")
	require.Empty(t, h.published)
	// And /events still refuses the carrier type by name.
	rec = h.post("/inst/acme/events", "", `{"device":"d","eventType":"IntegrationUplink","payload":{}}`, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeviceUplinkPlanIsCachedPerDevice(t *testing.T) {
	h := newDeviceHarness(t, fplan())
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "", "x", bearer).Code)
	}
	require.Equal(t, 1, h.plans.calls)
	require.Equal(t, http.StatusAccepted, h.post("/inst/acme/devices/meter-2/uplink", "", "x", bearer).Code)
	require.Equal(t, 2, h.plans.calls)
}

// 🔴 A CALLER WHO DOES NOT HOLD THE CREDENTIAL LEARNS NOTHING. Whether the device exists, and whether
// its type has a decoder, change nothing about the response to a wrong credential: the status, the
// body and the headers are the same, nothing is published, the body is never read, and the tenant's
// message ceiling is not spent (it belongs to the tenant the credential was proven for, not to the
// one the URL names). Each is paired with the request that holds the credential.
func TestDeviceUplinkUnauthenticatedRequestsAreIndistinguishable(t *testing.T) {
	garbage := map[string]string{"Authorization": "Bearer garbage"}
	withDecoder := newDeviceHarness(t, fplan())
	withoutDecoder := newDeviceHarness(t, nil)

	// The paired positives: the credential holder is told the truth, and the truth differs.
	require.Equal(t, http.StatusAccepted, withDecoder.post(uplinkPath, "", "x", bearer).Code)
	require.Equal(t, http.StatusUnprocessableEntity, withoutDecoder.post(uplinkPath, "", "x", bearer).Code)
	withDecoder.allowCalls, withoutDecoder.allowCalls = nil, nil
	withDecoder.received, withoutDecoder.received = 0, 0
	published := len(withDecoder.published)

	type seen struct {
		code   int
		body   string
		header string
	}
	var got []seen
	for name, h := range map[string]*deviceHarness{"a decoder is attached": withDecoder, "no decoder": withoutDecoder} {
		h.allowed = false // would be a 429 if the ceiling were consulted first
		for _, path := range []string{uplinkPath, "/inst/acme/devices/no-such-device/uplink", "/inst/victim/devices/meter-1/uplink"} {
			rec := h.post(path, "application/octet-stream", "\x01\x02", garbage)
			got = append(got, seen{rec.Code, rec.Body.String(), rec.Header().Get("WWW-Authenticate")})
			require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", name, path)
		}
		require.Empty(t, h.allowCalls, "%s: an unauthenticated request spends no tenant's ceiling", name)
		require.Zero(t, h.received, "%s: the body of an unauthenticated request is never read", name)
		require.Equal(t, []string{DeviceUplinkAccepted, DeviceUplinkNoDecoder}[boolIndex(name == "no decoder")], h.results[0], "the paired positive was counted")
	}
	for _, s := range got[1:] {
		require.Equal(t, got[0], s, "every unauthenticated response is the same response")
	}
	require.Len(t, withDecoder.published, published, "nothing was published for an unauthenticated request")
	require.Contains(t, withDecoder.results, DeviceUplinkUnauthorized)
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A device-management that cannot be asked is a retryable 503, never a 401 (the credential may be
// right) and never a 202.
func TestDeviceUplinkAuthenticationUnavailableIsRetryableNotRefusal(t *testing.T) {
	h := newDeviceHarness(t, fplan())
	h.plans.err = errors.New("dm down")
	rec := h.post(uplinkPath, "", "x", map[string]string{"Authorization": "Bearer garbage"})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Empty(t, h.published)
}

// The authenticated credential is what is asked: a request is authenticated as presented, and the
// same device with another credential is a separate question.
func TestDeviceUplinkAsksTheCredentialItPresented(t *testing.T) {
	h := newDeviceHarness(t, fplan())
	require.Equal(t, http.StatusAccepted, h.post(uplinkPath, "", "x", bearer).Code)
	require.Equal(t, http.StatusUnauthorized, h.post(uplinkPath, "", "x", map[string]string{"Authorization": "Bearer tok-2"}).Code,
		"another credential for the same device is not served the first one's answer")
	require.Equal(t, 2, h.plans.calls)
}
