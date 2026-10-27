// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/stretchr/testify/require"
)

// THE INTEGRATION ROUTE, driven end to end through the HTTP handler with a scripted
// device-management behind it. Every refusal is paired with the request that is accepted, so a
// route that refuses everything cannot pass.

const (
	goodBearer = "right-token-0123456789abcdefghijklmnopqrstuvwxy"
	csPath     = "/inst-1/acme/integrations/cs"
)

type routeBackend struct {
	mu        sync.Mutex
	authCalls atomic.Int64
	// good is the one bearer that authenticates; enabled says whether the integration is on.
	good    string
	enabled bool
	desc    func() integration.Descriptor
	down    bool
	// resolve answers resolveIntegrationDevice; the default resolves every identifier.
	resolve   func(scheme, value string) (integration.Resolution, error)
	undecoded []string
}

func (b *routeBackend) Auth(_ context.Context, _, slug, bearer string) (*integration.Descriptor, error) {
	b.authCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return nil, errors.New("device-management is unreachable")
	}
	if slug != "cs" || bearer != b.good {
		return nil, nil
	}
	d := integration.Descriptor{Token: slug, Type: integration.ChirpStackV4, Enabled: b.enabled, Identity: "DEV_EUI"}
	if b.desc != nil {
		d = b.desc()
		d.Token, d.Enabled = slug, b.enabled
	}
	return &d, d.Prepare()
}

func (b *routeBackend) Resolve(_ context.Context, _, _, _, scheme, value string) (integration.Resolution, error) {
	if b.resolve != nil {
		return b.resolve(scheme, value)
	}
	return integration.Resolution{Outcome: integration.Resolved, DeviceToken: "dev-" + strings.ToLower(value)}, nil
}

func (b *routeBackend) RecordUndecoded(_ context.Context, _, slug, _ string, count int, sample string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.undecoded = append(b.undecoded, fmt.Sprintf("%s:%d:%s", slug, count, sample))
	return nil
}

func (b *routeBackend) RecordAuthFailure(context.Context, string, string) error { return nil }

type publishedEvent struct {
	source  string
	tenant  string
	event   model.UnresolvedEvent
	payload interface{}
	dedupID string
}

type routeHarness struct {
	t         *testing.T
	es        *HttpEventSource
	backend   *routeBackend
	route     *IntegrationRoute
	published []publishedEvent
	failed    []error
	mu        sync.Mutex
	metrics   map[string]int

	allowCalls    atomic.Int64
	readingsCalls atomic.Int64
	allowOK       atomic.Bool
	readingsOK    atomic.Bool
	admitErr      error
	publishErr    error
	now           time.Time
}

func (h *routeHarness) count(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.metrics[key]
}

func newRouteHarness(t *testing.T) *routeHarness {
	t.Helper()
	h := &routeHarness{t: t, metrics: map[string]int{}, now: time.Date(2022, 7, 18, 9, 34, 16, 0, time.UTC)}
	h.allowOK.Store(true)
	h.readingsOK.Store(true)
	h.backend = &routeBackend{good: goodBearer, enabled: true}

	// 🔴 BOTH GATES ASSERT THE ORIGIN. An integration request metered as anything else would draw on
	// the allowance captured MQTT traffic spends, or on the unauthenticated HTTP pool.
	allow := func(_ string, _ string, _ time.Time, redelivery bool, origin Origin) bool {
		h.allowCalls.Add(1)
		if origin != OriginIntegration || redelivery {
			t.Errorf("the message gate was charged with origin %d, want OriginIntegration", origin)
		}
		return h.allowOK.Load()
	}
	readings := func(_ string, _ string, _ time.Time, redelivery bool, origin Origin, n int) bool {
		h.readingsCalls.Add(1)
		if origin != OriginIntegration || redelivery || n < 1 {
			t.Errorf("the reading gate was charged %d with origin %d, want OriginIntegration", n, origin)
		}
		return h.readingsOK.Load()
	}
	es, _, _ := newTestHttpSourceGated(t, allow, readings, func(string) error { return h.admitErr }, config.HttpIngest{}, nil)
	es.failed = func(_, _ string, _ []byte, err error) error {
		h.mu.Lock()
		h.failed = append(h.failed, err)
		h.mu.Unlock()
		return nil
	}
	h.es = es

	clock := func() time.Time { return h.now }
	h.route = &IntegrationRoute{
		Auth:      integration.NewAuthenticator(h.backend, integration.AuthConfig{Now: clock}),
		Devices:   integration.NewDevices(h.backend, integration.DeviceConfig{Now: clock}),
		Undecoded: integration.NewUndecoded(h.backend, time.Minute, clock, nil),
		Sources:   sourcelimit.Must(sourcelimit.Limits{Burst: 100000, Every: time.Millisecond, MaxKeys: 1000}),
		Seal: func(tenant, slug, bearer string) ([]byte, error) {
			return []byte("sealed|" + tenant + "|" + slug + "|" + bearer), nil
		},
		Publish: func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, dedupID string) error {
			if h.publishErr != nil {
				return h.publishErr
			}
			h.mu.Lock()
			h.published = append(h.published, publishedEvent{source, tenant, *event, payload, dedupID})
			h.mu.Unlock()
			return nil
		},
		Metrics: IntegrationMetrics{
			Request: func(typ, result string) { h.bump("request/" + typ + "/" + result) },
			Uplink:  func(result string) { h.bump("uplink/" + result) },
		},
		Now: clock,
	}
	require.NoError(t, es.UseIntegrations(h.route))
	return h
}

func (h *routeHarness) bump(key string) {
	h.mu.Lock()
	h.metrics[key]++
	h.mu.Unlock()
}

func (h *routeHarness) post(path, bearer, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, m := range mutate {
		m(r)
	}
	rec := httptest.NewRecorder()
	h.es.handler().ServeHTTP(rec, r)
	return rec
}

func fixtureBody(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../integration/testdata/" + name)
	require.NoError(t, err)
	return string(b)
}

const upQuery = "?event=up"

func (h *routeHarness) up() *httptest.ResponseRecorder {
	return h.post(csPath+upQuery, goodBearer, fixtureBody(h.t, "chirpstack/up_decoded.json"))
}

func (h *routeHarness) published1() publishedEvent {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Len(h.t, h.published, 1)
	return h.published[0]
}

func (h *routeHarness) nothingPublished() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Empty(h.t, h.published, "an event was published")
}

func TestAnIntegrationUplinkIsPublishedAsAnOrdinaryMeasurementMarkedAsTheIntegrations(t *testing.T) {
	h := newRouteHarness(t)
	rec := h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	p := h.published1()
	e := p.event
	require.Equal(t, "acme", p.tenant)
	require.Equal(t, eventtype.Measurement, e.EventType, "an integration publishes measurements only")
	require.Equal(t, "dev-0101010101010101", e.Device, "the device the alias resolved to, not anything in the body")
	require.Equal(t, "cs", e.Integration)
	require.Equal(t, []byte("sealed|acme|cs|"+goodBearer), e.SealedIntegrationToken)
	require.Equal(t, "integration:cs", e.Source)
	require.NotNil(t, e.AltId)
	require.Equal(t, "cs:3ac7e3c4-4401-4b8d-9386-a5c902f9202d", *e.AltId)
	require.Equal(t, "acme:cs:3ac7e3c4-4401-4b8d-9386-a5c902f9202d", p.dedupID, "the broker dedups on tenant:AltId")
	require.Equal(t, time.Date(2022, 7, 18, 9, 34, 15, 775023242, time.UTC), e.OccurredTime)
	require.Equal(t, h.now, e.ProcessedTime)

	// No other authority marker, and no device credential.
	require.False(t, e.BrokerAsserted)
	require.False(t, e.AuthenticatedTransport)
	require.Empty(t, e.Gateway)
	require.Nil(t, e.CredentialType)
	require.Nil(t, e.CredentialId)
	require.Nil(t, e.CredentialSecret)
	require.Nil(t, e.SealedCredentialId)
	require.Nil(t, e.SealedCredentialSecret)

	payload := p.payload.(*model.UnresolvedMeasurementsPayload)
	require.Len(t, payload.Entries, 1)
	require.Equal(t, "21.5", payload.Entries[0].Measurements["temperature"])
	require.Equal(t, "3.31", payload.Entries[0].Measurements["battery.voltage"])
	require.NotContains(t, payload.Entries[0].Measurements, "lorawan.rssi", "link metrics are off unless the integration asks")
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/accepted"))
	require.Equal(t, 1, h.count("uplink/published"))
}

func TestLinkMetricsAreStoredWhenTheIntegrationAsksForThem(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		return integration.Descriptor{Type: integration.ChirpStackV4, Identity: "DEV_EUI", LinkMetrics: true}
	}
	require.Equal(t, http.StatusAccepted, h.up().Code)
	m := h.published1().payload.(*model.UnresolvedMeasurementsPayload).Entries[0].Measurements
	require.Equal(t, "-36", m["lorawan.rssi"])
	require.Equal(t, "2", m["lorawan.gateways"])
}

// WRONG TOKEN: 401, nothing published, and the tenant's message gate is never spent. The right
// token, paired, is accepted and published once.
func TestAWrongTokenIsRefusedBeforeTheTenantsAllowanceOrAnythingElse(t *testing.T) {
	h := newRouteHarness(t)
	body := fixtureBody(t, "chirpstack/up_decoded.json")

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"a wrong token":                        h.post(csPath+upQuery, "wrong-token", body),
		"no header":                            h.post(csPath+upQuery, "", body),
		"the token in the URL, not the header": h.post(csPath+upQuery+"&token="+goodBearer, "", body),
		"a Basic scheme": h.post(csPath+upQuery, "", body, func(r *http.Request) {
			r.Header.Set("Authorization", "Basic "+goodBearer)
		}),
		"an unknown integration": h.post("/inst-1/acme/integrations/nope"+upQuery, goodBearer, body),
	} {
		require.Equal(t, http.StatusUnauthorized, rec.Code, name)
	}
	h.nothingPublished()
	require.Zero(t, h.allowCalls.Load(), "a sender that cannot authenticate spent the tenant's message allowance")
	require.Zero(t, h.readingsCalls.Load())
	require.Zero(t, h.count("uplink/published"))

	require.Equal(t, http.StatusAccepted, h.up().Code, "the paired positive")
	h.published1()
	require.EqualValues(t, 1, h.allowCalls.Load())
}

func TestASprayOfBadTokensLeavesAGoodTokenAccepted(t *testing.T) {
	h := newRouteHarness(t)
	require.Equal(t, http.StatusAccepted, h.up().Code)
	before := h.backend.authCalls.Load()

	limited := 0
	for i := 0; i < 1000; i++ {
		rec := h.post(csPath+upQuery, fmt.Sprintf("guess-%d", i), "{}")
		switch rec.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			limited++
			require.Equal(t, "1", rec.Header().Get("Retry-After"))
		default:
			t.Fatalf("a bad token was answered %d", rec.Code)
		}
	}
	require.Equal(t, 970, limited)
	require.EqualValues(t, 30, h.backend.authCalls.Load()-before, "device-management saw the burst, not the spray")
	require.Equal(t, http.StatusAccepted, h.up().Code, "the good token is still accepted")
	require.EqualValues(t, 2, h.allowCalls.Load(), "the two good requests were the only ones to reach the tenant gate")
	require.Equal(t, 970, h.count("request/unknown/bad_auth_limited"))
}

// DISABLED IS NOT BAD AUTH: refused with its own status and its own count, and it never charges
// the integration's bad-credential bucket (a hundred of them, then a wrong token is still a 401
// and not a 429).
func TestADisabledIntegrationIsRefusedWithoutBeingCountedAsBadAuth(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.enabled = false
	for i := 0; i < 100; i++ {
		require.Equal(t, http.StatusForbidden, h.up().Code)
	}
	h.nothingPublished()
	require.Equal(t, 100, h.count("request/CHIRPSTACK_V4/disabled"))
	require.Zero(t, h.count("request/unknown/unauthorized"))
	require.Zero(t, h.allowCalls.Load(), "a switched-off integration spends nothing of the tenant's")
	rec := h.post(csPath+upQuery, "wrong", "{}")
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the disabled requests were not charged to the bad-credential bucket")
}

// REVOCATION INSIDE THE CACHE WINDOW. The pre-check still remembers the token, but device-
// management refuses the lookup: nothing is published. Paired with the enabled case.
func TestARevocationInsideTheCacheWindowIsRefusedAtTheLookup(t *testing.T) {
	h := newRouteHarness(t)
	require.Equal(t, http.StatusAccepted, h.up().Code, "the paired positive: enabled")
	h.published1()

	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Disabled}, nil
	}
	body := strings.Replace(fixtureBody(t, "chirpstack/up_decoded.json"), "0101010101010101", "0202020202020202", 1)
	rec := h.post(csPath+upQuery, goodBearer, body)
	require.Equal(t, http.StatusForbidden, rec.Code)
	h.mu.Lock()
	require.Len(t, h.published, 1, "nothing more was published")
	h.mu.Unlock()
}

func TestATokenRotatedInsideTheCacheWindowIsRefusedAtTheLookup(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Unauthenticated}, nil
	}
	rec := h.up()
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	h.nothingPublished()
	// The pre-check's memory of the token was dropped, so the next request asks again.
	before := h.backend.authCalls.Load()
	h.up()
	require.Greater(t, h.backend.authCalls.Load(), before)
}

// SCOPE. A device outside the integration's scope reads as unregistered: counted and skipped,
// never resolved. The in-scope device is published.
func TestADeviceOutOfScopeIsAnUnregisteredSkipNotAPublish(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Unregistered}, nil
	}
	require.Equal(t, http.StatusAccepted, h.up().Code, "an unknown device is a counted skip, not an error")
	h.nothingPublished()
	require.Equal(t, 1, h.count("uplink/unknown_device"))
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/accepted"))

	h.backend.resolve = nil
	body := strings.Replace(fixtureBody(t, "chirpstack/up_decoded.json"), "0101010101010101", "0303030303030303", 1)
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)
	require.Equal(t, "dev-0303030303030303", h.published1().event.Device)
}

// ANY error from the device lookup is 503 and a refusal to publish: an old schema ("field not
// defined"), a token that cannot be minted, a timeout. None of them is the sender's credential.
func TestAnyErrorFromTheDeviceLookupFailsClosedAs503(t *testing.T) {
	for name, err := range map[string]error{
		"an old schema":     errors.New(`graphql: Cannot query field "resolveIntegrationDevice" on type "Mutation"`),
		"no service token":  errors.New("svcclient: could not obtain a service token"),
		"a timeout":         context.DeadlineExceeded,
		"an unknown answer": errors.New("device-management answered an unknown outcome \"MAYBE\""),
	} {
		h := newRouteHarness(t)
		h.backend.resolve = func(string, string) (integration.Resolution, error) { return integration.Resolution{}, err }
		rec := h.up()
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
		h.nothingPublished()
		require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/resolve_unavailable"), name)
		require.Zero(t, h.count("request/CHIRPSTACK_V4/unauthorized"), name)
	}
}

// UNDECODED. A frame the network server did not decode is 202, counted, recorded on the
// integration with a sample, and never published. (Mutant: a silent ack with no count is red.)
func TestAnUplinkWithNoDecodedObjectIsAcceptedCountedAndRecorded(t *testing.T) {
	h := newRouteHarness(t)
	rec := h.post(csPath+upQuery, goodBearer, fixtureBody(t, "chirpstack/up_no_object.json"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	h.nothingPublished()
	require.Equal(t, 1, h.count("uplink/undecoded"))
	require.Eventually(t, func() bool {
		h.backend.mu.Lock()
		defer h.backend.mu.Unlock()
		return len(h.backend.undecoded) == 1
	}, 2*time.Second, 5*time.Millisecond)
	h.backend.mu.Lock()
	require.Equal(t, "cs:1:aa", h.backend.undecoded[0], "the marker names the integration and samples the frame")
	h.backend.mu.Unlock()

	// An object of only strings has no readings either.
	body := strings.Replace(fixtureBody(t, "chirpstack/up_decoded.json"), `"temperature": 21.50,`, ``, 1)
	_ = body
	strOnly := strings.Replace(fixtureBody(t, "chirpstack/up_no_object.json"), `"rxInfo"`, `"object": {"s": "x"}, "rxInfo"`, 1)
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, strOnly).Code)
	require.Equal(t, 2, h.count("uplink/undecoded"))
	h.nothingPublished()
}

// SHED. A tenant over its ceiling is answered 202 and counted, never 429, and nothing is
// published. The paired positive: within the ceiling, published.
func TestATenantOverItsCeilingIsAnswered202AndCounted(t *testing.T) {
	h := newRouteHarness(t)
	h.allowOK.Store(false)
	rec := h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, "a 429 loses the message too, and a network server can suspend the webhook")
	h.nothingPublished()
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/shed_accepted"))

	h.allowOK.Store(true)
	h.readingsOK.Store(false)
	rec = h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, "the same at the reading stage")
	h.nothingPublished()
	require.Equal(t, 1, h.count("uplink/shed"))

	h.readingsOK.Store(true)
	require.Equal(t, http.StatusAccepted, h.up().Code)
	h.published1()
}

func TestRequestsThatAreNotUplinksAreIgnoredWithA204(t *testing.T) {
	h := newRouteHarness(t)
	rec := h.post(csPath+"?event=join", goodBearer, fixtureBody(t, "chirpstack/join.json"))
	require.Equal(t, http.StatusNoContent, rec.Code)
	h.nothingPublished()
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/ignored_type"))
}

func TestARefusedBodyIsReportedAndNeverPublished(t *testing.T) {
	h := newRouteHarness(t)

	rec := h.post(csPath+upQuery, goodBearer, "not json")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Len(t, h.failed, 1, "a malformed body is routed to failed-decode like any other")

	rec = h.post(csPath+upQuery, goodBearer, fixtureBody(t, "chirpstack/up_decoded.json"), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/octet-stream")
	})
	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	require.Contains(t, rec.Body.String(), "JSON")

	rec = h.post(csPath+upQuery, goodBearer, strings.Repeat("a", integration.MaxBodyBytes+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	bad := strings.Replace(fixtureBody(t, "chirpstack/up_decoded.json"), "0101010101010101", "01", 1)
	require.Equal(t, http.StatusBadRequest, h.post(csPath+upQuery, goodBearer, bad).Code, "a malformed DevEUI is a 400")
	h.nothingPublished()

	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/unsupported_encoding"))
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/too_large"))
	require.Equal(t, 2, h.count("request/CHIRPSTACK_V4/bad_envelope"))
}

func TestAnUplinkOverTheReadingLimitIsRefusedWholeAndNeverTruncated(t *testing.T) {
	h := newRouteHarness(t)
	var sb strings.Builder
	sb.WriteString(`{"deduplicationId":"3ac7e3c4-4401-4b8d-9386-a5c902f9202d","time":"2022-07-18T09:34:15Z","deviceInfo":{"devEui":"0101010101010101"},"object":{`)
	for i := 0; i < 257; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"k%03d":1`, i)
	}
	sb.WriteString(`}}`)
	rec := h.post(csPath+upQuery, goodBearer, sb.String())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "refused whole")
	h.nothingPublished()
	require.Equal(t, 1, h.count("uplink/too_many_readings"))
	require.Len(t, h.failed, 1)
	require.ErrorIs(t, h.failed[0], model.ErrTooManyReadings)
}

func TestBackpressureAndAFailedPublishAreAnswered503(t *testing.T) {
	h := newRouteHarness(t)
	h.admitErr = messaging.ErrStreamBackpressure
	rec := h.up()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"), "a refusal is certainly not stored")
	h.nothingPublished()
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/backpressure"))

	h.admitErr = nil
	h.publishErr = errors.New("nats timeout")
	rec = h.up()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Empty(t, rec.Header().Get("Retry-After"), "a failed publish may have been stored")
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/publish_failed"))

	h.publishErr = nil
	require.Equal(t, http.StatusAccepted, h.up().Code)
}

func TestAPreCheckOutageServesTheLastKnownGoodAnswerOrAnswers503(t *testing.T) {
	h := newRouteHarness(t)
	require.Equal(t, http.StatusAccepted, h.up().Code)

	h.backend.mu.Lock()
	h.backend.down = true
	h.backend.mu.Unlock()
	h.now = h.now.Add(5 * time.Minute)
	require.Equal(t, http.StatusAccepted, h.up().Code, "a known-good answer rides out the outage")
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/precheck_stale"))

	h.now = h.now.Add(11 * time.Minute)
	rec := h.up()
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 1, h.count("request/unknown/precheck_unavailable"))
	h.mu.Lock()
	require.Len(t, h.published, 2, "the refused request published nothing")
	h.mu.Unlock()

	// With nothing remembered at all.
	h2 := newRouteHarness(t)
	h2.backend.down = true
	require.Equal(t, http.StatusServiceUnavailable, h2.up().Code)
}

func TestWithoutARouteTheEndpointAnswers503NotNotFound(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	r := httptest.NewRequest(http.MethodPost, csPath+upQuery, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, r)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "not available")

	require.Error(t, es.UseIntegrations(&IntegrationRoute{}), "a route built without its parts is refused")
}

func TestPathNamesAreValidated(t *testing.T) {
	h := newRouteHarness(t)
	for _, path := range []string{
		"/inst-1/bad.tenant/integrations/cs", "/inst-1/acme/integrations/bad.name",
		"/inst-1/acme/integrations/cs/" + strings.Repeat("a", 65),
	} {
		require.Equal(t, http.StatusBadRequest, h.post(path, goodBearer, "{}").Code, path)
	}
	require.Equal(t, http.StatusNotFound, h.post("/other-inst/acme/integrations/cs", goodBearer, "{}").Code,
		"another instance's URL is not served here")
}

func TestTheThingsStackPerMessagePathsAndGenericArrays(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		return integration.Descriptor{Type: integration.TTSV3, Identity: "DEV_EUI"}
	}
	h.now = time.Date(2020, 2, 12, 15, 15, 47, 0, time.UTC)
	body := fixtureBody(t, "tts/up_decoded.json")
	require.Equal(t, http.StatusNoContent, h.post(csPath+"/join-accept", goodBearer, fixtureBody(t, "tts/join_accept.json")).Code)
	require.Equal(t, http.StatusAccepted, h.post(csPath+"/up", goodBearer, body).Code)
	p := h.published1()
	require.Equal(t, "tts:0004A30B001C0530:1:2020-02-12T15:15:45.789585559Z", *p.event.AltId)
	require.Equal(t, 1, h.count("request/TTS_V3/ignored_type"))
}

func TestAGenericArrayPublishesEveryKnownItemAndSkipsTheRest(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		g := `{"idFrom":"pointer:/sensor","itemsPointer":"/readings","timePointer":"/ts"}`
		return integration.Descriptor{Type: integration.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	h.now = time.Date(2026, 10, 26, 10, 1, 0, 0, time.UTC)
	h.backend.resolve = func(_, value string) (integration.Resolution, error) {
		if value == "pump-8" {
			return integration.Resolution{Outcome: integration.Unregistered}, nil
		}
		return integration.Resolution{Outcome: integration.Resolved, DeviceToken: value}, nil
	}
	rec := h.post(csPath, goodBearer, fixtureBody(t, "generic/array.json"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, "pump-7", h.published1().event.Device)
	require.Equal(t, 1, h.count("uplink/unknown_device"))

	// All or nothing on a malformed item: nothing of the request is published.
	h2 := newRouteHarness(t)
	h2.backend.desc = h.backend.desc
	h2.now = h.now
	bad := strings.Replace(fixtureBody(t, "generic/array.json"), `"sensor": "pump-8"`, `"sensor": [1]`, 1)
	require.Equal(t, http.StatusBadRequest, h2.post(csPath, goodBearer, bad).Code)
	h2.nothingPublished()
}

func TestTheSameUplinkTwiceInOneRequestIsPublishedOnce(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		g := `{"idFrom":"pointer:/id","timePointer":"/t"}`
		return integration.Descriptor{Type: integration.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	item := `{"id":"p1","v":1,"t":"2022-07-18T09:34:00Z"}`
	require.Equal(t, http.StatusAccepted, h.post(csPath, goodBearer, "["+item+","+item+"]").Code)
	h.published1()
	require.Equal(t, 1, h.count("uplink/duplicate_suppressed"))
}

// ONE CHARGE CANNOT BUY A HUNDRED LOOKUPS. The message gate is charged once before the body is read
// and once more for each further uplink after the codec, before any device is looked up.
func TestEachUplinkOfAnArrayIsChargedToTheMessageGateBeforeAnyLookup(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		g := `{"idFrom":"pointer:/id"}`
		return integration.Descriptor{Type: integration.GenericJSON, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	lookups := 0
	h.backend.resolve = func(_, v string) (integration.Resolution, error) {
		lookups++
		return integration.Resolution{Outcome: integration.Resolved, DeviceToken: v}, nil
	}
	items := make([]string, 5)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"p%d","v":1}`, i)
	}
	body := "[" + strings.Join(items, ",") + "]"
	require.Equal(t, http.StatusAccepted, h.post(csPath, goodBearer, body).Code)
	require.EqualValues(t, 5, h.allowCalls.Load(), "one charge per uplink")
	require.Equal(t, 5, lookups)

	// The gate refusing partway: accepted and counted as a shed, with not one lookup made.
	h2 := newRouteHarness(t)
	h2.backend.desc = h.backend.desc
	n := 0
	h2.es.allow = func(string, string, time.Time, bool, Origin) bool { n++; return n < 3 }
	h2.backend.resolve = func(_, v string) (integration.Resolution, error) {
		t.Error("a lookup was made for a request the gate refused")
		return integration.Resolution{}, nil
	}
	require.Equal(t, http.StatusAccepted, h2.post(csPath, goodBearer, body).Code)
	h2.nothingPublished()
	require.Equal(t, 1, h2.count("request/GENERIC_JSON/shed_accepted"))
}

// A codec failure that is not the sender's mistake has a label of its own, not the pre-check's.
func TestACodecDefectIsCountedAsAnInternalErrorNotAPreCheckOutage(t *testing.T) {
	h := newRouteHarness(t)
	h.backend.desc = func() integration.Descriptor {
		g := `{"idFrom":"pointer:/id"}` // a binary integration cannot name a pointer into a body it cannot read
		return integration.Descriptor{Type: integration.GenericBinary, Identity: "ALIAS:custom.pump", Generic: &g}
	}
	rec := h.post(csPath, goodBearer, "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 1, h.count("request/GENERIC_BINARY/internal_error"))
	require.Zero(t, h.count("request/unknown/precheck_unavailable"))
}

// The route the chart's front end forwards: the public /api/integrations/... arrives here with
// no instance segment, and it must be the SAME route, not a lookalike.
func TestTheInstanceLessPathIsTheSameRouteAsTheQualifiedOne(t *testing.T) {
	h := newRouteHarness(t)
	rec := h.post("/integrations/acme/cs"+upQuery, goodBearer, fixtureBody(t, "chirpstack/up_decoded.json"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	p := h.published1()
	require.Equal(t, "acme", p.tenant)
	require.Equal(t, "integration:cs", p.event.Source)

	// The message-type form, and the same refusals: no token, a bad name.
	require.Equal(t, http.StatusUnauthorized, h.post("/integrations/acme/cs/up", "", "{}").Code)
	require.Equal(t, http.StatusBadRequest, h.post("/integrations/bad.tenant/cs", goodBearer, "{}").Code)
	require.Equal(t, http.StatusBadRequest, h.post("/integrations/acme/cs/"+strings.Repeat("a", 65), goodBearer, "{}").Code)
}

// NEGATIVE CONTROL: the instance-less form reaches the integration handler and nothing else.
// /events is not reachable without the instance, and the integration route takes POST only.
func TestTheInstanceLessPathReachesNoOtherRoute(t *testing.T) {
	h := newRouteHarness(t)
	for _, path := range []string{"/integrations/acme/events", "/acme/events", "/integrations/acme", "/integrations"} {
		rec := h.post(path, goodBearer, fixtureBody(t, "chirpstack/up_decoded.json"))
		require.NotEqual(t, http.StatusAccepted, rec.Code, path)
		h.nothingPublished()
	}
	// "events" there is an integration NAME, authenticated like any other, never the ingest route.
	require.Equal(t, http.StatusUnauthorized, h.post("/integrations/acme/events", goodBearer, "{}").Code)

	// A traversal out of the integration path is cleaned by the mux and redirected, never served
	// as the device-ingest route.
	for _, path := range []string{"/integrations/../inst-1/acme/events", "/integrations/acme/../../inst-1/acme/events"} {
		rec := h.post(path, goodBearer, fixtureBody(t, "chirpstack/up_decoded.json"))
		require.NotEqual(t, http.StatusAccepted, rec.Code, path)
		h.nothingPublished()
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		r := httptest.NewRequest(method, "/integrations/acme/cs", nil)
		rec := httptest.NewRecorder()
		h.es.handler().ServeHTTP(rec, r)
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, method)
	}
}

// The route is public and its names and tokens are the sender's. A spray of invented ones must
// cost device-management a bounded number of calls, however it is spread across addresses.

func fromAddr(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr + ":4000" }
}

func TestASprayOfInventedNamesFromOneSourceIsBoundedByTheSourceBeforeDeviceManagement(t *testing.T) {
	h := newRouteHarness(t)
	h.route.Sources = sourcelimit.Must(sourcelimit.Limits{Burst: 10, Every: time.Hour, MaxKeys: 100})
	// A vouched integration, proven before the spray.
	require.Equal(t, http.StatusAccepted, h.up().Code)
	base := h.backend.authCalls.Load()

	limited := 0
	for i := 0; i < 200; i++ {
		rec := h.post(fmt.Sprintf("/inst-1/t%d/integrations/n%d?event=up", i, i), fmt.Sprintf("guess-%d", i), "{}", fromAddr("198.51.100.7"))
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	require.LessOrEqual(t, h.backend.authCalls.Load()-base, int64(10), "more calls to device-management than the source's burst")
	require.GreaterOrEqual(t, limited, 190)
	require.GreaterOrEqual(t, h.count("request/unknown/source_limited"), 190)

	// The vouched integration's steady traffic is answered from memory and is not charged to
	// anyone, the spraying source's address included.
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, fixtureBody(t, "chirpstack/up_decoded.json"), fromAddr("198.51.100.7")).Code)
}

func TestASprayFromManySourcesIsBoundedByTheReplicaBudgetAndLeavesAVouchedIntegrationAlone(t *testing.T) {
	h := newRouteHarness(t)
	clock := func() time.Time { return h.now }
	h.route.Auth = integration.NewAuthenticator(h.backend, integration.AuthConfig{Now: clock, UnvouchedBurst: 5, UnvouchedRate: 0.001})
	body := fixtureBody(t, "chirpstack/up_decoded.json")
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body).Code)
	base := h.backend.authCalls.Load()

	for i := 0; i < 300; i++ {
		src := fmt.Sprintf("203.0.%d.%d", i/200, i%200+1)
		rec := h.post(fmt.Sprintf("/inst-1/t%d/integrations/n%d?event=up", i, i), fmt.Sprintf("guess-%d", i), "{}", fromAddr(src))
		if i < 5 { // the budgeted few reach device-management and are refused there
			require.Equal(t, http.StatusUnauthorized, rec.Code, "request %d", i)
		} else {
			require.Equal(t, http.StatusTooManyRequests, rec.Code, "request %d", i)
		}
	}
	require.LessOrEqual(t, h.backend.authCalls.Load()-base, int64(5), "more calls to device-management than the replica budget")
	require.GreaterOrEqual(t, h.count("request/unknown/precheck_budget"), 295)

	// The vouched integration: still accepted, from a fresh address, with its token remembered,
	// and again after the remembered answer has gone stale (a vouched name is never budgeted).
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body, fromAddr("192.0.2.200")).Code)
	h.now = h.now.Add(5 * time.Minute)
	require.Equal(t, http.StatusAccepted, h.post(csPath+upQuery, goodBearer, body, fromAddr("192.0.2.201")).Code)
	// A rotated token for the vouched name is a question device-management is asked, not a guess
	// the budget refuses.
	require.Equal(t, http.StatusUnauthorized, h.post(csPath+upQuery, "rotated", body, fromAddr("192.0.2.202")).Code)
}

// A source address is the peer unless the peer is a trusted proxy, which a sender cannot
// make true by writing a header.
func TestTheSourceBucketBelievesForwardedForOnlyFromATrustedProxy(t *testing.T) {
	h := newRouteHarness(t)
	h.route.Sources = sourcelimit.Must(sourcelimit.Limits{Burst: 2, Every: time.Hour, MaxKeys: 100})
	h.route.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	send := func(peer, xff string, i int) int {
		return h.post(fmt.Sprintf("/inst-1/acme/integrations/n%d", i), "guess", "{}", fromAddr(peer),
			func(r *http.Request) { r.Header.Set("X-Forwarded-For", xff) }).Code
	}
	// Behind the proxy, two clients have two allowances; one client has one.
	for i := 0; i < 2; i++ {
		require.NotEqual(t, http.StatusTooManyRequests, send("10.1.1.1", "198.51.100.1", i))
		require.NotEqual(t, http.StatusTooManyRequests, send("10.1.1.1", "198.51.100.2", i))
	}
	require.Equal(t, http.StatusTooManyRequests, send("10.1.1.1", "198.51.100.1", 9))
	// From an untrusted peer the header is ignored: varying it buys no new allowance.
	for i := 0; i < 2; i++ {
		send("203.0.113.9", fmt.Sprintf("198.51.100.%d", 50+i), i)
	}
	require.Equal(t, http.StatusTooManyRequests, send("203.0.113.9", "198.51.100.99", 9))
}
