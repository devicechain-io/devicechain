// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-ai-inference/model"
	aischema "github.com/devicechain-io/dc-ai-inference/schema"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// fakeConsent is a scriptable ConsentChecker that records whether it was consulted.
type fakeConsent struct {
	enabled bool
	err     error
	calls   int
}

func (f *fakeConsent) ExternalEnabled(context.Context, string) (bool, error) {
	f.calls++
	return f.enabled, f.err
}

// panicConsent fails the test if consulted — used to prove the operator smoke-test
// path never touches the tenant-consent gate.
type panicConsent struct{ t *testing.T }

func (p panicConsent) ExternalEnabled(context.Context, string) (bool, error) {
	p.t.Fatal("consent must not be consulted on the operator smoke-test path")
	return false, nil
}

// fakeRate is a scriptable RateGate that records how many times it was charged, so a
// test can prove the gate is consulted exactly once per resolution and only after the
// cheap local gates.
type fakeRate struct {
	allow  bool
	calls  int
	tenant string
}

func (f *fakeRate) Allow(tenant string) bool {
	f.calls++
	f.tenant = tenant
	return f.allow
}

// panicRate fails the test if charged — used to prove a path never spends budget.
type panicRate struct{ t *testing.T }

func (p panicRate) Allow(string) bool {
	p.t.Fatal("the rate gate must not be charged on this path")
	return false
}

// newHarness builds a real in-memory Api (the ai_providers table + the secret store,
// via the production migrations) plus a resolver over the given consent checker. The
// rate gate is nil (unmetered) — see newHarnessWithRate.
func newHarness(t *testing.T, consent ConsentChecker) (*Resolver, *model.Api, context.Context) {
	t.Helper()
	return newHarnessWithRate(t, consent, nil)
}

// newHarnessWithRate is newHarness with an explicit per-tenant rate gate.
func newHarnessWithRate(t *testing.T, consent ConsentChecker, rate RateGate) (*Resolver, *model.Api, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	require.NoError(t, aischema.NewAIProvidersSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	api := model.NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
	bounds := Bounds{MaxOutputTokens: 100, MaxPromptBytes: 1000, Timeout: 5 * time.Second}
	return NewResolver(api, consent, rate, bounds, nil), api, context.Background()
}

func strp(s string) *string { return &s }

// makeProvider creates a provider (optionally with an endpoint override + key) and
// returns nothing — the caller looks it up by token.
func makeProvider(t *testing.T, api *model.Api, ctx context.Context, token, endpoint, secret string, enabled bool) {
	t.Helper()
	req := &model.AIProviderCreateRequest{
		Token:   token,
		Kind:    string(model.AIProviderKindAnthropic),
		Model:   "claude-opus-4-8",
		Enabled: enabled,
	}
	if endpoint != "" {
		req.Endpoint = strp(endpoint)
	}
	if secret != "" {
		req.Secret = strp(secret)
	}
	_, err := api.CreateAIProvider(ctx, req)
	require.NoError(t, err)
}

// cannedServer returns a server that answers the Messages API with fixed text.
func cannedServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"claude-opus-4-8","content":[{"type":"text","text":"CANDIDATE"}]}`))
	}))
}

func TestResolveForTenantNoTenant(t *testing.T) {
	r, _, ctx := newHarness(t, &fakeConsent{enabled: true})
	_, err := r.ResolveForTenant(ctx, "  ")
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestResolveForTenantNoActive(t *testing.T) {
	consent := &fakeConsent{enabled: true}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", "", "sk", true) // created but NOT activated
	_, err := r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, consent.calls, "no active provider short-circuits before the consent network call")
}

func TestResolveForTenantDisabledActive(t *testing.T) {
	consent := &fakeConsent{enabled: true}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", "", "sk", false)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)
	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, consent.calls, "a disabled active provider is rejected before the consent check")
}

func TestResolveForTenantConsentDenied(t *testing.T) {
	consent := &fakeConsent{enabled: false}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)
	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrConsentRequired)
	assert.Equal(t, 1, consent.calls)
}

func TestResolveForTenantConsentError(t *testing.T) {
	consent := &fakeConsent{err: errors.New("um down")}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)
	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable, "a consent-check error must fail closed, never allow")
}

func TestResolveForTenantNoKey(t *testing.T) {
	consent := &fakeConsent{enabled: true}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", "", "", true) // enabled, opted-in, but NO key
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)
	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, 1, consent.calls, "consent is checked before the key resolves")
}

func TestResolveForTenantSuccess(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	consent := &fakeConsent{enabled: true}
	r, api, ctx := newHarness(t, consent)
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)

	resolved, err := r.ResolveForTenant(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, "p", resolved.Token)
	out, err := resolved.Provider.Infer(ctx, Input{Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "CANDIDATE", out.Candidate)
}

func TestResolveProviderSmokeTestNoConsent(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	// A DISABLED provider — the operator smoke test still resolves it (validate before
	// enabling); the panicConsent proves consent is never consulted here.
	r, api, ctx := newHarness(t, panicConsent{t})
	makeProvider(t, api, ctx, "p", srv.URL, "sk", false)

	resolved, err := r.ResolveProvider(ctx, "p")
	require.NoError(t, err)
	out, err := resolved.Provider.Infer(ctx, Input{Prompt: "ping"})
	require.NoError(t, err)
	assert.Equal(t, "CANDIDATE", out.Candidate)
}

func TestResolveProviderNoKey(t *testing.T) {
	r, api, ctx := newHarness(t, panicConsent{t})
	makeProvider(t, api, ctx, "p", "", "", false) // no key → never call out unauthenticated
	_, err := r.ResolveProvider(ctx, "p")
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestResolveProviderMissing(t *testing.T) {
	r, _, ctx := newHarness(t, panicConsent{t})
	_, err := r.ResolveProvider(ctx, "nope")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// A tenant over its inference rate ceiling is shed with the distinct, transient
// ErrRateLimited sentinel — not the coarse ErrUnavailable, which would tell an author
// to go find an operator for something that resolves by waiting a moment.
func TestResolveForTenantRateLimited(t *testing.T) {
	consent := &fakeConsent{enabled: true}
	rate := &fakeRate{allow: false}
	r, api, ctx := newHarnessWithRate(t, consent, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)

	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.Equal(t, 1, rate.calls, "the gate is charged exactly once per resolution")
	assert.Equal(t, "t1", rate.tenant, "the ceiling is charged against the CALLING tenant")
}

// ORDERING, the load-bearing part. The rate gate must sit in FRONT of the consent
// check: consent is deliberately uncached (so a revocation takes effect at once), which
// makes it one user-management query per call — a caller looping the door would turn
// its loop into a user-management flood. Shedding first keeps the loop in memory.
func TestResolveForTenantRateLimitPrecedesConsent(t *testing.T) {
	consent := &fakeConsent{enabled: true}
	rate := &fakeRate{allow: false}
	r, api, ctx := newHarnessWithRate(t, consent, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)

	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.Zero(t, consent.calls, "a shed call must not reach the uncached consent network call")
}

// The consent gate still runs on every call that is UNDER the ceiling, so putting rate
// first cannot let data cross the boundary un-consented — it changes only the message
// an abusive caller sees, never the security invariant.
func TestResolveForTenantRateAllowedStillChecksConsent(t *testing.T) {
	consent := &fakeConsent{enabled: false}
	rate := &fakeRate{allow: true}
	r, api, ctx := newHarnessWithRate(t, consent, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)

	_, err = r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrConsentRequired, "consent is still enforced under the ceiling")
	assert.Equal(t, 1, consent.calls)
}

// Budget is only charged past the cheap local gates: a call that could never have been
// served (no active provider) must not consume the tenant's ceiling.
func TestResolveForTenantNoActiveDoesNotChargeBudget(t *testing.T) {
	r, api, ctx := newHarnessWithRate(t, &fakeConsent{enabled: true}, panicRate{t})
	makeProvider(t, api, ctx, "p", "", "sk", true) // created but NOT activated
	_, err := r.ResolveForTenant(ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// Likewise a request with no tenant: there is nothing to charge.
func TestResolveForTenantNoTenantDoesNotChargeBudget(t *testing.T) {
	r, _, ctx := newHarnessWithRate(t, &fakeConsent{enabled: true}, panicRate{t})
	_, err := r.ResolveForTenant(ctx, "  ")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// The operator smoke test is instance-scoped config validation, not a tenant spending
// its own budget, so it never charges the per-tenant ceiling (it has no tenant to
// charge). It resolves through ResolveProvider, which skips both gates by design.
func TestResolveProviderSmokeTestDoesNotChargeBudget(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	r, api, ctx := newHarnessWithRate(t, panicConsent{t}, panicRate{t})
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	resolved, err := r.ResolveProvider(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, "p", resolved.Token)
}

// A resolver built with no gate is unmetered — the tests-only escape hatch. main always
// wires one, since the platform default is itself a limit.
func TestResolveForTenantNilRateGateIsUnmetered(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	r, api, ctx := newHarnessWithRate(t, &fakeConsent{enabled: true}, nil)
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	_, err := api.SetActiveProvider(ctx, "p")
	require.NoError(t, err)
	_, err = r.ResolveForTenant(ctx, "t1")
	assert.NoError(t, err)
}

func TestValidatePrompt(t *testing.T) {
	r, _, _ := newHarness(t, &fakeConsent{})
	assert.Error(t, r.ValidatePrompt("", "   "), "empty prompt rejected")
	assert.Error(t, r.ValidatePrompt("", strings.Repeat("x", 1001)), "oversized prompt rejected")
	assert.NoError(t, r.ValidatePrompt("sys", "draft a rule"))
}

// WIRE CONTRACT. event-processing classifies a transient rate-limit by matching this
// substring on the returned GraphQL error message (svcclient surfaces only the message
// text, not error extensions), so it can tell an author "wait a moment" instead of the
// generic "unavailable, go find an operator".
//
// If this text changes, update `rateLimitMarker` in
// event-processing/processor/nl_inference_client.go. Drift degrades benignly — the
// author just sees the vaguer reason — but it silently loses the better message.
func TestErrRateLimitedCarriesTheWireMarker(t *testing.T) {
	assert.Contains(t, ErrRateLimited.Error(), "inference rate limit exceeded")
}
