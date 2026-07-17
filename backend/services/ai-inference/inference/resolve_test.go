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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// testTier is the tier every harness tenant sits at unless a test says otherwise.
const testTier = "gold"

// fakeFacts is a scriptable TenantFactsReader that records whether it was consulted —
// which is how the ordering tests below prove a shed call never reaches the network.
type fakeFacts struct {
	enabled bool
	tier    string
	err     error
	calls   int
}

func (f *fakeFacts) Facts(context.Context, string) (TenantFacts, error) {
	f.calls++
	if f.err != nil {
		return TenantFacts{}, f.err
	}
	tier := f.tier
	if tier == "" {
		tier = testTier
	}
	return TenantFacts{ExternalEnabled: f.enabled, TierToken: tier}, nil
}

// panicFacts fails the test if consulted — used to prove the operator smoke-test path
// never reads tenant facts (no consent gate, no tier).
type panicFacts struct{ t *testing.T }

func (p panicFacts) Facts(context.Context, string) (TenantFacts, error) {
	p.t.Fatal("tenant facts must not be read on the operator smoke-test path")
	return TenantFacts{}, nil
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

// newHarness builds a real in-memory Api (the provider + grant tables and the secret
// store, via the production migrations) plus a resolver over the given facts reader.
// The rate gate is nil (unmetered) — see newHarnessWithRate.
func newHarness(t *testing.T, facts TenantFactsReader) (*Resolver, *model.Api, context.Context) {
	t.Helper()
	return newHarnessWithRate(t, facts, nil)
}

// newHarnessWithRate is newHarness with an explicit per-tenant rate gate.
//
// The returned context carries a TENANT, because that is what the production context
// carries on this path and the difference is load-bearing rather than cosmetic: the
// data-plane handler stamps the tenant from the verified token
// (core/graphql/handler.go), the inference mutation passes that same context down, and
// the additive-grant read is tenant-scoped — so a context without a tenant fails
// closed. A harness that resolved under a bare context would exercise a credential
// that cannot exist and would prove nothing about the isolation it appears to test.
func newHarnessWithRate(t *testing.T, facts TenantFactsReader, rate RateGate) (*Resolver, *model.Api, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	require.NoError(t, aischema.NewAIProvidersSchema().Migrate(db))
	require.NoError(t, aischema.NewAIProviderGrantsSchema().Migrate(db))
	require.NoError(t, aischema.NewAIFunctionAssignmentsSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	api := model.NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
	bounds := Bounds{MaxOutputTokens: 100, MaxPromptBytes: 1000, Timeout: 5 * time.Second}
	return NewResolver(api, facts, rate, bounds, nil), api, core.WithTenant(context.Background(), "t1")
}

// offer grants a provider to the harness tier AND designates it the platform baseline —
// the minimum setup for a tenant that has assigned nothing to still resolve a model.
// Both halves are needed and they are different questions: the grant is the ENTITLEMENT
// (without it the baseline is filtered out by the menu), the baseline is the ANSWER
// (without it a tenant that chose nothing resolves to nothing). Writes go through the
// system context, as the operator admin plane does.
func offer(t *testing.T, api *model.Api, token string) {
	t.Helper()
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, token))
	require.NoError(t, api.SetPlatformBaseline(context.Background(), token))
}

// assign points the harness tenant's rule-drafting function at a provider.
func assign(t *testing.T, api *model.Api, token string) {
	t.Helper()
	require.NoError(t, api.SetFunctionModel(context.Background(), "t1", model.FunctionRuleDrafting, token))
}

// resolve calls the production path for the one function the platform declares.
func resolve(r *Resolver, ctx context.Context, tenant string) (*Resolved, error) {
	return r.ResolveForFunction(ctx, tenant, model.FunctionRuleDrafting)
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

func TestResolveForFunctionNoTenant(t *testing.T) {
	r, _, ctx := newHarness(t, &fakeFacts{enabled: true})
	_, err := resolve(r, ctx, "  ")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// A provider registered but granted to nobody serves nobody: registering a model and
// selling it are separate acts. This is also the LOCAL short-circuit — the property the
// retired active-pointer read used to give for free — so it must cost no network call.
func TestResolveForFunctionNothingGranted(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "sk", true) // created but granted to no tier
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Zero(t, facts.calls,
		"an instance that grants nothing must answer without touching user-management")
}

// A tenant whose TIER offers nothing is unavailable even though other tiers have
// models — that is the entitlement axis doing its job, and it is the case the retired
// global pointer could not express at all.
func TestResolveForFunctionTierOffersNothing(t *testing.T) {
	facts := &fakeFacts{enabled: true, tier: "bronze"}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p") // granted to `gold`, not to bronze

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable, "a bronze tenant must not reach a gold model")
}

// The tier's only model being DISABLED is unavailable. Note this one DOES reach
// user-management first, unlike the case above: which models a tenant has depends on
// its tier, so there is nothing local to decide it on. The short-circuit that survives
// is the coarse "this instance grants nothing at all" — a per-tier answer inherently
// needs the tier.
func TestResolveForFunctionOnlyModelDisabled(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "sk", false)
	offer(t, api, "p")
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// Models offered, but the tenant assigned none and no baseline is designated: the menu
// is non-empty and yet no model can be chosen for the caller. Guessing — "there is only
// one, use it" — would route the tenant's prompts, and its spend, to a model nobody
// picked, and is the exact rule that shipped as a bug five times. Fail closed.
func TestResolveForFunctionNoAssignmentAndNoBaseline(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "a", "", "sk", true)
	makeProvider(t, api, ctx, "b", "", "sk", true)
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, "a"))
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, "b"))

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable, "an unchosen model must not be guessed")
}

// And the single-model case lands identically. This is the arm a counting rule would
// have "helpfully" answered — one model on the menu, entitled, enabled, and still NONE,
// because nobody said to use it.
func TestResolveForFunctionASoleGrantedModelIsNotAnAnswer(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "a", "", "sk", true)
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, "a"))

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable,
		"a sole granted model is an entitlement, not a choice — nothing may infer a default from the menu's size")
}

// The tenant's ASSIGNMENT is what answers, not merely whatever is on the menu, and not
// the baseline either — an explicit choice outranks the platform's fallback.
func TestResolveForFunctionUsesTheAssignedModel(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "cheap", srv.URL, "sk", true)
	makeProvider(t, api, ctx, "premium", srv.URL, "sk", true)
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, "cheap"))
	require.NoError(t, api.GrantProviderToTier(context.Background(), testTier, "premium"))
	// `cheap` is the instance baseline; t1 explicitly chose `premium`.
	require.NoError(t, api.SetPlatformBaseline(context.Background(), "cheap"))
	assign(t, api, "premium")

	resolved, err := resolve(r, ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, "premium", resolved.Token,
		"the tenant's assignment answers — not the first model on the menu, and not the baseline")
}

// A per-tenant additive grant reaches the resolver: the audited exception is real at
// the point it matters, not just in the admin surface.
func TestResolveForFunctionHonoursAnAdditiveGrant(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	facts := &fakeFacts{enabled: true, tier: "bronze"}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "fable", srv.URL, "sk", true)
	// bronze offers nothing; t1 is granted fable as an exception and assigns it. Being
	// the sole model on its menu is NOT what makes it answer — the assignment is.
	require.NoError(t, api.GrantProviderToTenant(context.Background(), "t1", "fable"))
	assign(t, api, "fable")

	resolved, err := resolve(r, ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, "fable", resolved.Token,
		"a bronze tenant given Fable as an exception can use it (ADR-065 decision 7)")
}

func TestResolveForFunctionConsentDenied(t *testing.T) {
	facts := &fakeFacts{enabled: false}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p")
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrConsentRequired)
	assert.Equal(t, 1, facts.calls)
}

func TestResolveForFunctionConsentError(t *testing.T) {
	facts := &fakeFacts{err: errors.New("um down")}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p")
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable, "a consent-check error must fail closed, never allow")
}

func TestResolveForFunctionNoKey(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", "", "", true) // enabled, opted-in, but NO key
	offer(t, api, "p")
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, 1, facts.calls, "consent is checked before the key resolves")
}

func TestResolveForFunctionSuccess(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	facts := &fakeFacts{enabled: true}
	r, api, ctx := newHarness(t, facts)
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	offer(t, api, "p")

	resolved, err := resolve(r, ctx, "t1")
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
	r, api, ctx := newHarness(t, panicFacts{t})
	makeProvider(t, api, ctx, "p", srv.URL, "sk", false)

	resolved, err := r.ResolveProvider(ctx, "p")
	require.NoError(t, err)
	out, err := resolved.Provider.Infer(ctx, Input{Prompt: "ping"})
	require.NoError(t, err)
	assert.Equal(t, "CANDIDATE", out.Candidate)
}

func TestResolveProviderNoKey(t *testing.T) {
	r, api, ctx := newHarness(t, panicFacts{t})
	makeProvider(t, api, ctx, "p", "", "", false) // no key → never call out unauthenticated
	_, err := r.ResolveProvider(ctx, "p")
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestResolveProviderMissing(t *testing.T) {
	r, _, ctx := newHarness(t, panicFacts{t})
	_, err := r.ResolveProvider(ctx, "nope")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// A tenant over its inference rate ceiling is shed with the distinct, transient
// ErrRateLimited sentinel — not the coarse ErrUnavailable, which would tell an author
// to go find an operator for something that resolves by waiting a moment.
func TestResolveForFunctionRateLimited(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	rate := &fakeRate{allow: false}
	r, api, ctx := newHarnessWithRate(t, facts, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p")

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.Equal(t, 1, rate.calls, "the gate is charged exactly once per resolution")
	assert.Equal(t, "t1", rate.tenant, "the ceiling is charged against the CALLING tenant")
}

// ORDERING, the load-bearing part. The rate gate must sit in FRONT of the consent
// check: consent is deliberately uncached (so a revocation takes effect at once), which
// makes it one user-management query per call — a caller looping the door would turn
// its loop into a user-management flood. Shedding first keeps the loop in memory.
func TestResolveForFunctionRateLimitPrecedesConsent(t *testing.T) {
	facts := &fakeFacts{enabled: true}
	rate := &fakeRate{allow: false}
	r, api, ctx := newHarnessWithRate(t, facts, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p")

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.Zero(t, facts.calls, "a shed call must not reach the uncached consent network call")
}

// The consent gate still runs on every call that is UNDER the ceiling, so putting rate
// first cannot let data cross the boundary un-consented — it changes only the message
// an abusive caller sees, never the security invariant.
func TestResolveForFunctionRateAllowedStillChecksConsent(t *testing.T) {
	facts := &fakeFacts{enabled: false}
	rate := &fakeRate{allow: true}
	r, api, ctx := newHarnessWithRate(t, facts, rate)
	makeProvider(t, api, ctx, "p", "", "sk", true)
	offer(t, api, "p")

	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrConsentRequired, "consent is still enforced under the ceiling")
	assert.Equal(t, 1, facts.calls)
}

// Budget is only charged past the cheap local gates: a call that could never have been
// served (no active provider) must not consume the tenant's ceiling.
func TestResolveForFunctionNoActiveDoesNotChargeBudget(t *testing.T) {
	r, api, ctx := newHarnessWithRate(t, &fakeFacts{enabled: true}, panicRate{t})
	makeProvider(t, api, ctx, "p", "", "sk", true) // created but NOT activated
	_, err := resolve(r, ctx, "t1")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// Likewise a request with no tenant: there is nothing to charge.
func TestResolveForFunctionNoTenantDoesNotChargeBudget(t *testing.T) {
	r, _, ctx := newHarnessWithRate(t, &fakeFacts{enabled: true}, panicRate{t})
	_, err := resolve(r, ctx, "  ")
	assert.ErrorIs(t, err, ErrUnavailable)
}

// The operator smoke test is instance-scoped config validation, not a tenant spending
// its own budget, so it never charges the per-tenant ceiling (it has no tenant to
// charge). It resolves through ResolveProvider, which skips both gates by design.
func TestResolveProviderSmokeTestDoesNotChargeBudget(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	r, api, ctx := newHarnessWithRate(t, panicFacts{t}, panicRate{t})
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	resolved, err := r.ResolveProvider(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, "p", resolved.Token)
}

// A resolver built with no gate is unmetered — the tests-only escape hatch. main always
// wires one, since the platform default is itself a limit.
func TestResolveForFunctionNilRateGateIsUnmetered(t *testing.T) {
	srv := cannedServer(t)
	defer srv.Close()
	r, api, ctx := newHarnessWithRate(t, &fakeFacts{enabled: true}, nil)
	makeProvider(t, api, ctx, "p", srv.URL, "sk", true)
	offer(t, api, "p")
	_, err := resolve(r, ctx, "t1")
	assert.NoError(t, err)
}

func TestValidatePrompt(t *testing.T) {
	r, _, _ := newHarness(t, &fakeFacts{})
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
