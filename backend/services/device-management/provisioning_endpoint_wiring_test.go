// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/provisioning"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔴 NOTHING ELSE EXERCISES main.go's WIRING, so this does. It assembles the globals the way
// the service's AfterRdb and AfterNats do (the Api through buildApis, with a broker, so the
// provisioning Checker is the one main.go builds; the metrics through buildMetrics' line),
// calls the SAME wireProvisioningEndpoint the service calls, and drives the mux the service
// serves. A handler that was never mounted, mounted on the wrong mux, mounted without its
// limits, or mounted over the wrong Api answers a 404 or registers nothing here.

type provWiring struct {
	t   *testing.T
	api *model.Api
	srv *httptest.Server
}

// wireProvisioning builds the service's globals for the endpoint and wires it. withKeys false
// builds the Api without the enrollment keys, as an instance with no root key would.
func wireProvisioning(t *testing.T, mutate func(*config.DeviceManagementConfiguration), withKeys bool, infra ...func(*core.Microservice)) *provWiring {
	t.Helper()
	prevMs, prevCfg, prevApi, prevMetrics, prevGates := Microservice, Configuration, Api, ProvisioningMetrics, safetyGates
	t.Cleanup(func() {
		Microservice, Configuration, Api, ProvisioningMetrics, safetyGates = prevMs, prevCfg, prevApi, prevMetrics, prevGates
	})

	Microservice = &core.Microservice{InstanceId: "inst1", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	for _, f := range infra {
		f(Microservice)
	}
	safetyGates = governance.NewSafetyGates(Microservice)
	Configuration = config.NewDeviceManagementConfiguration()
	if mutate != nil {
		mutate(Configuration)
	}
	require.NoError(t, Configuration.Validate())

	host, port := startEmbeddedNats(t)
	nmgr := startNatsManager(t, host, port, fmt.Sprintf("provendpoint%d", time.Now().UnixNano()))
	enrollment := dmtest.EnrollmentKeys()
	if !withKeys {
		enrollment = nil
	}
	var err error
	Api, _, err = buildApis(nmgr, &rdb.RdbManager{Database: putest.NewSQLiteDB(t, credentialTables()...)},
		dmtest.DeviceSecretKey(), enrollment, Configuration)
	require.NoError(t, err)
	buildProvisioningMetrics()

	require.NoError(t, wireProvisioningEndpoint())
	srv := httptest.NewServer(Microservice.Mux())
	t.Cleanup(srv.Close)
	return &provWiring{t: t, api: Api, srv: srv}
}

func (w *provWiring) post(tenant string, body map[string]any) (int, map[string]any) {
	w.t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(w.t, err)
	resp, err := http.Post(w.srv.URL+"/provisioning/"+tenant+"/register", "application/json", bytes.NewReader(raw))
	require.NoError(w.t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return resp.StatusCode, m
}

// The route is on the microservice's own mux, under the pattern the handler declares, and
// answers a registration end to end: a profile made through the Api is registered against over
// HTTP and the device exists afterwards.
func TestTheRegistrationRouteIsMountedAndRegisters(t *testing.T) {
	w := wireProvisioning(t, nil, true)

	_, pattern := Microservice.Mux().Handler(httptest.NewRequest(http.MethodPost, "/provisioning/acme/register", nil))
	require.Equal(t, provisioning.Path, pattern)

	ctx := core.WithTenant(t.Context(), "acme")
	_, err := w.api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	created, err := w.api.CreateProvisioningProfile(ctx, &model.ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", Strategy: string(model.ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true})
	require.NoError(t, err)

	body := map[string]any{"provisionKey": "fleet-key", "provisionSecret": created.IssuedSecret,
		"externalId": "SN-1", "credentialSecret": strings.Repeat("a", 43)}
	status, got := w.post("acme", body)
	require.Equal(t, 201, status, "%v", got)
	assert.Equal(t, "CREATED", got["outcome"])
	assert.Equal(t, "inst1:acme:"+core.DeriveDeviceToken("SN-1", "pv-"), got["mqtt"].(map[string]any)["clientId"])
	devices, err := w.api.DevicesByExternalId(ctx, []string{"SN-1"})
	require.NoError(t, err)
	require.Len(t, devices, 1)

	// The metrics the service built are the ones the handler counts on.
	assert.Equal(t, 1.0, testutil.ToFloat64(ProvisioningMetrics.Requests.WithLabelValues(provisioning.ResultCreated)))
	status, _ = w.post("acme", body)
	assert.Equal(t, 200, status, "the same request again is a REPLAYED")

	// A GET is refused by the mux, which proves the route is method-scoped through main's mux.
	rec := httptest.NewRecorder()
	Microservice.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/provisioning/acme/register", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// An instance without the keys it compares under answers 503, NEVER 404: the route stays
// mounted, because a 404 reads as a wrong URL.
func TestWithoutKeysTheRouteAnswers503Not404(t *testing.T) {
	w := wireProvisioning(t, nil, false)
	status, got := w.post("acme", map[string]any{"provisionKey": "k", "provisionSecret": "s",
		"externalId": "SN-1", "credentialSecret": strings.Repeat("a", 43)})
	assert.Equal(t, 503, status)
	assert.Equal(t, "PROVISIONING_UNAVAILABLE", got["code"])
}

// The mounted configuration reaches the handler: a one-request source burst is enforced on the
// service's mux. A service that stopped passing the setting would run the defaults while every
// handler test stayed green.
func TestTheConfiguredLimitsReachTheHandler(t *testing.T) {
	w := wireProvisioning(t, func(c *config.DeviceManagementConfiguration) {
		c.Provisioning.SourceBurst = 1
		c.Provisioning.SourceEvery = "1h"
	}, true)
	body := map[string]any{"provisionKey": "k", "provisionSecret": "s", "externalId": "SN-1", "credentialSecret": strings.Repeat("a", 43)}
	status, _ := w.post("acme", body)
	assert.Equal(t, 401, status)
	status, got := w.post("acme", body)
	assert.Equal(t, 429, status)
	assert.Equal(t, "RATE_LIMITED", got["code"])
}

// post with extra headers, for the source-address tests.
func (w *provWiring) postFrom(xff string, body map[string]any) int {
	w.t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(w.t, err)
	req, err := http.NewRequest(http.MethodPost, w.srv.URL+"/provisioning/acme/register", bytes.NewReader(raw))
	require.NoError(w.t, err)
	req.Header.Set("X-Forwarded-For", xff)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(w.t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// The service reads infrastructure.http.trustedProxies and hands it to the endpoint: with the
// loopback peer trusted, X-Forwarded-For names separate sources with a bucket each, and without
// it the same headers are one source. Both halves, so that neither a handler that ignores the
// list nor a service that forgets to pass it can pass.
func TestTheConfiguredTrustedProxiesReachTheEndpoint(t *testing.T) {
	burst := func(c *config.DeviceManagementConfiguration) {
		c.Provisioning.SourceBurst = 1
		c.Provisioning.SourceEvery = "1h"
	}
	body := map[string]any{"provisionKey": "k", "provisionSecret": "s", "externalId": "SN-1", "credentialSecret": strings.Repeat("a", 43)}

	t.Run("trusted peer: one bucket per forwarded client", func(t *testing.T) {
		w := wireProvisioning(t, burst, true, func(ms *core.Microservice) {
			ms.InstanceConfiguration.Infrastructure.Http.TrustedProxies = []string{"127.0.0.0/8", "::1/128"}
		})
		assert.Equal(t, 401, w.postFrom("203.0.113.1", body))
		assert.Equal(t, 401, w.postFrom("203.0.113.2", body), "a second client shares the first one's bucket")
		assert.Equal(t, 429, w.postFrom("203.0.113.1", body))
	})
	t.Run("no trusted proxy: the header is ignored", func(t *testing.T) {
		w := wireProvisioning(t, burst, true)
		assert.Equal(t, 401, w.postFrom("203.0.113.1", body))
		assert.Equal(t, 429, w.postFrom("203.0.113.2", body), "an unbelieved header bought a fresh bucket")
	})
}

// The lifecycle gate the service builds is the one the endpoint consults: a tenant it calls
// deleted gets the generic 401 for a request that would otherwise register, and another tenant
// registers. A service that stopped passing the gate would register into a tenant being erased.
func TestTheTenantLifecycleGateReachesTheEndpoint(t *testing.T) {
	prev := provisioningTenantGate
	t.Cleanup(func() { provisioningTenantGate = prev })
	provisioningTenantGate = func(mscfg.InfrastructureConfiguration) func(string) bool {
		return func(tenant string) bool { return tenant == "acme" }
	}
	w := wireProvisioning(t, nil, true)
	ctx := core.WithTenant(t.Context(), "acme")
	_, err := w.api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	created, err := w.api.CreateProvisioningProfile(ctx, &model.ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "fleet-key", Strategy: string(model.ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true})
	require.NoError(t, err)
	body := map[string]any{"provisionKey": "fleet-key", "provisionSecret": created.IssuedSecret,
		"externalId": "SN-1", "credentialSecret": strings.Repeat("a", 43)}

	status, got := w.post("acme", body)
	assert.Equal(t, 401, status, "a deleted tenant registered a device: %v", got)
	assert.Equal(t, "PROVISIONING_FAILED", got["code"])
	devices, err := w.api.DevicesByExternalId(ctx, []string{"SN-1"})
	require.NoError(t, err)
	assert.Empty(t, devices)
}

// The per-tenant ceiling is the Provisioning governance dimension: with no user-management
// configured it is the platform default, which is a limit and not "unlimited".
func TestThePerTenantCeilingIsTheProvisioningDimension(t *testing.T) {
	wireProvisioning(t, nil, true)
	l := buildProvisioningTenantLimiter(Microservice.InstanceConfiguration.Infrastructure)
	def := governance.DefaultProvisioningLimits()
	allowed := 0
	for i := 0; i < def.Burst+50; i++ {
		if l.Allow("acme") {
			allowed++
		}
	}
	assert.GreaterOrEqual(t, allowed, def.Burst)
	assert.Less(t, allowed, def.Burst+50, "the default ceiling did not limit")
}

// An empty infrastructure.http.trustedProxies is warned about once at startup, naming the
// consequence; naming a proxy silences it; a malformed entry stops the service.
func TestAnEmptyTrustedProxyListIsWarnedAbout(t *testing.T) {
	// wire runs the service's own wiring step with the given trusted proxies and returns what
	// it logged. Each call is a subtest because the shared sink captures one test at a time.
	wire := func(t *testing.T, proxies []string) (string, error) {
		logs := logSink.Capture(t)
		prevMs, prevCfg, prevApi, prevMetrics, prevGates := Microservice, Configuration, Api, ProvisioningMetrics, safetyGates
		t.Cleanup(func() {
			Microservice, Configuration, Api, ProvisioningMetrics, safetyGates = prevMs, prevCfg, prevApi, prevMetrics, prevGates
		})
		Microservice = &core.Microservice{InstanceId: "inst1", FunctionalArea: "device-management"}
		Microservice.UseMetricsRegistry(prometheus.NewRegistry())
		Microservice.InstanceConfiguration.Infrastructure.Http.TrustedProxies = proxies
		safetyGates = governance.NewSafetyGates(Microservice)
		Configuration = config.NewDeviceManagementConfiguration()
		Api = model.NewApi(&rdb.RdbManager{})
		buildProvisioningMetrics()
		err := wireProvisioningEndpoint()
		return logs.String(), err
	}

	t.Run("empty warns once", func(t *testing.T) {
		out, err := wire(t, nil)
		require.NoError(t, err)
		assert.Contains(t, out, `"level":"warn"`)
		assert.Contains(t, out, "infrastructure.http.trustedProxies is empty")
		assert.Contains(t, out, "every device behind it shares one bucket")
		assert.Equal(t, 1, strings.Count(out, "trustedProxies is empty"), "warned more than once")
	})
	t.Run("a named proxy is quiet", func(t *testing.T) {
		out, err := wire(t, []string{"10.42.0.0/16"})
		require.NoError(t, err)
		assert.Contains(t, out, "Device self-registration endpoint mounted", "the capture saw nothing, so silence proves nothing")
		assert.NotContains(t, out, "trustedProxies is empty")
	})
	t.Run("a malformed entry stops the service", func(t *testing.T) {
		_, err := wire(t, []string{"not-a-cidr"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "infrastructure.http.trustedProxies")
	})
}
