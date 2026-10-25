// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 🔴 EVERYTHING HERE DRIVES THE REAL HANDLER OVER A REAL SOCKET AGAINST THE REAL MODEL (an
// in-memory SQLite stands in for Postgres, which is the model suite's own fixture). What a
// test asserts is the status, the body and the counter a client or an operator would see,
// and the number of secret compares a request paid for, observed at the Checker.

// material is a valid registration material, distinct per n.
func material(n int) string { return fmt.Sprintf("material-%030d", n) }

// countingRegistrar records what the handler asked of the model, so a test can tell a request
// that never reached authentication from one that did.
type countingRegistrar struct {
	Registrar
	auth, refuse, provision atomic.Int64
	// hold, when set, blocks AuthenticateProvisioningProfile until it is closed, so a test
	// can keep a slot occupied.
	hold    chan struct{}
	entered chan struct{}
	// failWith, when set, is returned from ProvisionDevice instead of registering.
	failWith error
}

func (c *countingRegistrar) AuthenticateProvisioningProfile(ctx context.Context, k, s string) (*model.ProvisioningProfile, error) {
	c.auth.Add(1)
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.hold != nil {
		<-c.hold
	}
	return c.Registrar.AuthenticateProvisioningProfile(ctx, k, s)
}

func (c *countingRegistrar) RefuseProvisioning(ctx context.Context, k, s string) error {
	c.refuse.Add(1)
	return c.Registrar.RefuseProvisioning(ctx, k, s)
}

func (c *countingRegistrar) ProvisionDevice(ctx context.Context, r *model.ProvisionRequest) (*model.ProvisionResult, error) {
	c.provision.Add(1)
	if c.failWith != nil {
		return nil, c.failWith
	}
	return c.Registrar.ProvisionDevice(ctx, r)
}

// stubTenants is a per-tenant limiter that counts its charges and can be told to refuse.
type stubTenants struct {
	charged atomic.Int64
	deny    atomic.Bool
}

func (s *stubTenants) Allow(string) bool { s.charged.Add(1); return !s.deny.Load() }

type fixture struct {
	t         *testing.T
	api       *model.Api
	ctx       context.Context
	handler   *Handler
	srv       *httptest.Server
	metrics   *Metrics
	registrar *countingRegistrar
	stub      *stubTenants
	// compares counts every secret compare the Checker ran, the dummy included.
	compares atomic.Int64
	deleted  atomic.Bool
	cfg      Config
	deps     Deps
	secrets  map[string]string
	keys     map[string]string
}

type option func(*fixture)

func withConfig(f func(*Config)) option { return func(x *fixture) { f(&x.cfg) } }
func withTrusted(prefixes ...string) option {
	return func(x *fixture) {
		for _, p := range prefixes {
			x.deps.TrustedProxies = append(x.deps.TrustedProxies, netip.MustParsePrefix(p))
		}
	}
}
func withTenants(l TenantLimiter) option { return func(x *fixture) { x.deps.Tenants = l } }
func withoutKeys() option                { return func(x *fixture) { x.api.ProvisioningChecker = nil } }

func newFixture(t *testing.T, opts ...option) *fixture {
	t.Helper()
	tables := []any{&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.DeviceProfileActivation{}, &model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DetectionRuleScopeRef{}, &model.ProvisioningProfile{}, &model.DeviceCredential{}}
	api := model.NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, tables...)})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	api.EnrollmentKeys = dmtest.EnrollmentKeys()

	f := &fixture{t: t, api: api, ctx: putest.TenantContext("acme")(), stub: &stubTenants{},
		secrets: map[string]string{}, keys: map[string]string{},
		cfg: Config{ReadDeadline: 10 * time.Second, MaxInFlight: 8, SourceBurst: 1000, SourceEvery: time.Millisecond}}
	var err error
	api.ProvisioningChecker, err = credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindProvisioningProfile:   {Unthrottled: true},
		credential.KindProvisionedCredential: {Unthrottled: true},
	}, credential.WithDeviceSecretKey(dmtest.DeviceSecretKey()),
		credential.WithProvisioningSecretKey(dmtest.EnrollmentKeys().Provisioning),
		credential.WithCompareObserver(func([]byte) { f.compares.Add(1) }))
	require.NoError(t, err)

	ms := &core.Microservice{InstanceId: "inst1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	f.metrics = NewMetrics(ms)
	f.registrar = &countingRegistrar{Registrar: api}
	f.deps = Deps{
		Registrar: f.registrar, InstanceId: "inst1", Tenants: f.stub, Metrics: f.metrics,
		TenantDeleted: func(string) bool { return f.deleted.Load() },
	}

	for _, tok := range []string{"dt-a", "dt-b"} {
		_, err := api.CreateDeviceType(f.ctx, &model.DeviceTypeCreateRequest{Token: tok})
		require.NoError(t, err)
	}
	for _, o := range opts {
		o(f)
	}
	f.start()
	return f
}

// start (re)builds the handler and the server from the fixture's current config and deps.
func (f *fixture) start() {
	f.t.Helper()
	if f.srv != nil {
		f.srv.Close()
	}
	h, err := New(f.cfg, f.deps)
	require.NoError(f.t, err)
	f.handler = h
	mux := http.NewServeMux()
	Register(mux, h)
	f.srv = httptest.NewServer(mux)
	f.t.Cleanup(f.srv.Close)
}

// profile creates a profile and remembers its issued secret.
func (f *fixture) profile(token string, strategy model.ProvisioningStrategy, mutate ...func(*model.ProvisioningProfileCreateRequest)) {
	f.t.Helper()
	req := &model.ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "key-" + token, Strategy: string(strategy), DeviceTypeToken: "dt-a", Enabled: true,
	}
	for _, m := range mutate {
		m(req)
	}
	created, err := f.api.CreateProvisioningProfile(f.ctx, req)
	require.NoError(f.t, err)
	f.secrets[token], f.keys[token] = created.IssuedSecret, req.ProvisionKey
}

// device creates a device, earmarked for profile when it is not "".
func (f *fixture) device(token, external, deviceType, profile string) {
	f.t.Helper()
	req := &model.DeviceCreateRequest{Token: token, ExternalId: &external, DeviceTypeToken: deviceType}
	if profile != "" {
		req.ProvisioningProfileToken = &profile
	}
	_, err := f.api.CreateDevice(f.ctx, req)
	require.NoError(f.t, err)
}

func (f *fixture) sql(query string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).Exec(query, args...).Error)
}

// body is a valid request for profile, which a test mutates one field of.
func (f *fixture) body(profile, external string, n int) map[string]any {
	return map[string]any{"provisionKey": f.keys[profile], "provisionSecret": f.secrets[profile],
		"externalId": external, "credentialSecret": material(n)}
}

// reply is one response, read in full.
type reply struct {
	Status int
	Header http.Header
	Raw    []byte
}

func (r reply) JSON() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.Raw, &m); err != nil {
		panic(fmt.Sprintf("not JSON: %q: %v", r.Raw, err))
	}
	return m
}

func (r reply) Code() string { s, _ := r.JSON()["code"].(string); return s }

// post sends body (a map, marshalled; or []byte/string sent as is) to the tenant's endpoint.
func (f *fixture) post(tenant string, body any, headers ...string) reply {
	f.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case []byte:
		raw = b
	case string:
		raw = []byte(b)
	default:
		var err error
		raw, err = json.Marshal(b)
		require.NoError(f.t, err)
	}
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/provisioning/"+tenant+"/register", bytes.NewReader(raw))
	require.NoError(f.t, err)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := f.srv.Client().Do(req)
	require.NoError(f.t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(f.t, err)
	return reply{Status: resp.StatusCode, Header: resp.Header, Raw: out}
}

// count is the current value of a result's counter.
func (f *fixture) count(result string) float64 {
	return testutil.ToFloat64(f.metrics.Requests.WithLabelValues(result))
}

// counted returns every result with a non-zero count.
func (f *fixture) counted() map[string]float64 {
	out := map[string]float64{}
	for _, r := range Results {
		if v := f.count(r); v != 0 {
			out[r] = v
		}
	}
	return out
}

// rows counts the rows a registration could have written.
func (f *fixture) rows() (devices, credentials int64) {
	f.t.Helper()
	db := f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(f.t, db.Raw(`SELECT count(*) FROM devices`).Scan(&devices).Error)
	require.NoError(f.t, db.Raw(`SELECT count(*) FROM device_credentials`).Scan(&credentials).Error)
	return
}
