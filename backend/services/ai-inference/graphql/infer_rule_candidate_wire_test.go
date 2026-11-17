// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-ai-inference/model"
	aischema "github.com/devicechain-io/dc-ai-inference/schema"
	"github.com/devicechain-io/dc-microservice/aiwire"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type wireFacts struct {
	err     error
	consent bool
	calls   int
}

func (f *wireFacts) Facts(_ context.Context, tenant string) (inference.TenantFacts, error) {
	f.calls++
	if tenant != "wire-tenant" {
		return inference.TenantFacts{}, fmt.Errorf("unexpected tenant %q", tenant)
	}
	return inference.TenantFacts{ExternalEnabled: f.consent, TierToken: "gold"}, f.err
}

type wireTransport func(*http.Request) (*http.Response, error)

func (f wireTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type wireRate bool

func (r wireRate) Allow(string) bool { return bool(r) }

// Inspect the serialized response, including extensions, rather than only Message.
// Consent and retry advice are public; resolution gates and topology are private.
func inferenceWireLeaks(body []byte) bool {
	text := strings.ToLower(string(body))
	for _, private := range []string{
		"http:", "https:", "um.internal", "provider.internal",
		"could not read", "could not resolve", "no provider is granted",
		"no entitled model", "no api key", "rule-drafting",
	} {
		if strings.Contains(text, private) {
			return true
		}
	}
	return false
}

func TestInferRuleCandidateWire(t *testing.T) {
	factsErr := errors.New("GET http://um.internal:8080/graphql: connection refused")
	providerErr := errors.New("dial https://provider.internal:8443: connection refused")
	for _, tc := range []struct {
		name                                       string
		factsErr                                   error
		providerErr                                error
		noGrant, denyConsent, rateLimited, timeout bool
		wantErr                                    error
		code, outcome                              string
		factsCalls, providerCalls                  int
	}{
		{name: "facts error", factsErr: factsErr, wantErr: inference.ErrUnavailable, outcome: outcomeUnavailable, factsCalls: 1},
		{name: "provider error", providerErr: providerErr, wantErr: inference.ErrUnavailable, outcome: outcomeProviderError, factsCalls: 1, providerCalls: 1},
		{name: "no grant", noGrant: true, wantErr: inference.ErrUnavailable, outcome: outcomeUnavailable},
		{name: "consent denied", denyConsent: true, wantErr: inference.ErrConsentRequired, outcome: outcomeConsentRequired, factsCalls: 1},
		{name: "rate limited", rateLimited: true, wantErr: inference.ErrRateLimited, code: aiwire.CodeRateLimited, outcome: outcomeRateLimited},
		{name: "timed out", timeout: true, wantErr: inference.ErrTimedOut, code: aiwire.CodeTimedOut, outcome: outcomeTimedOut, factsCalls: 1, providerCalls: 1},
		{name: "success", outcome: outcomeServed, factsCalls: 1, providerCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, rdb.RegisterTenantScoping(db))
			require.NoError(t, rdb.RegisterTokenGrammar(db))
			for _, migration := range aischema.Migrations {
				require.NoError(t, migration.Migrate(db))
			}
			key, err := secrets.NewInstanceKeyProvider([]byte("0123456789abcdef0123456789abcdef"))
			require.NoError(t, err)
			api := model.NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, key))
			endpoint, secret := "https://provider.internal:8443", "wire-secret"
			_, err = api.CreateAIProvider(context.Background(), &model.AIProviderCreateRequest{
				Token: "wire-provider", Kind: string(model.AIProviderKindAnthropic),
				Model: "wire-model", Enabled: true, Endpoint: &endpoint, Secret: &secret,
			})
			require.NoError(t, err)
			if !tc.noGrant {
				require.NoError(t, api.GrantProviderToTier(context.Background(), "gold", "wire-provider"))
				require.NoError(t, api.SetTierDefault(context.Background(), "gold", "wire-provider"))
			}
			facts := &wireFacts{err: tc.factsErr, consent: !tc.denyConsent}
			providerCalls := 0
			// Fake the provider's HTTP boundary so the real resolver still builds it
			// from the stored row and resolves its key. No network or product seam.
			client := &http.Client{Transport: wireTransport(func(req *http.Request) (*http.Response, error) {
				providerCalls++
				assert.Equal(t, endpoint+"/v1/messages", req.URL.String())
				if tc.timeout {
					<-req.Context().Done()
					return nil, fmt.Errorf("call %s: %w", endpoint, req.Context().Err())
				}
				if tc.providerErr != nil {
					return nil, tc.providerErr
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
					`{"model":"wire-model","content":[{"type":"text","text":"CANDIDATE"}]}`))}, nil
			})}
			metrics := &Metrics{
				calls:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ai_inference_calls_total", Help: "calls"}, []string{"outcome"}),
				tokens: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ai_inference_tokens_total", Help: "tokens"}, []string{"direction"}),
			}
			registry := prometheus.NewRegistry()
			registry.MustRegister(metrics.calls)
			resolver := inference.NewResolver(api, facts, wireRate(!tc.rateLimited), inference.Bounds{
				MaxPromptBytes: 1000, MaxOutputTokens: 100, Timeout: 20 * time.Millisecond,
			}, client)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{Inference: resolver, Metrics: metrics})
			ctx := auth.WithClaims(core.WithTenant(context.Background(), "wire-tenant"), &auth.Claims{
				TokenType: auth.TokenTypeService, Authorities: []string{string(auth.AIInfer)},
			})
			resp := schema.Exec(ctx, `mutation ($request: InferenceRequest!) {
				inferRuleCandidate(request: $request) { candidate model provider }
			}`, "", map[string]any{"request": map[string]any{"prompt": "draft a rule"}})
			raw, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.False(t, inferenceWireLeaks(raw), "private detail in response: %s", raw)
			if tc.wantErr != nil {
				require.Len(t, resp.Errors, 1, "%s", raw)
				assert.JSONEq(t, `null`, string(resp.Data))
				assert.Equal(t, []any{"inferRuleCandidate"}, resp.Errors[0].Path)
				assert.Equal(t, tc.wantErr.Error(), resp.Errors[0].Message)
				if tc.code == "" {
					assert.NotContains(t, resp.Errors[0].Extensions, "code")
				} else {
					assert.Equal(t, tc.code, resp.Errors[0].Extensions["code"])
				}
			} else {
				require.Empty(t, resp.Errors, "%s", raw)
				assert.JSONEq(t, `{"inferRuleCandidate":{"candidate":"CANDIDATE","model":"wire-model","provider":"wire-provider"}}`, string(resp.Data))
			}
			assert.Equal(t, tc.factsCalls, facts.calls)
			assert.Equal(t, tc.providerCalls, providerCalls)
			families, err := registry.Gather()
			require.NoError(t, err)
			require.Len(t, families, 1)
			require.Len(t, families[0].Metric, 1, "exactly one outcome series per request")
			metric := families[0].Metric[0]
			require.Len(t, metric.Label, 1)
			assert.Equal(t, "outcome", metric.Label[0].GetName())
			assert.Equal(t, tc.outcome, metric.Label[0].GetValue())
			assert.Equal(t, float64(1), metric.Counter.GetValue(), "exactly one call counted")
		})
	}
}

func TestInferenceWireLeakDetectorNegativeControl(t *testing.T) {
	identity := func(err error) error { return err }
	for _, err := range []error{
		errors.New("GET http://um.internal:8080/graphql: connection refused"),
		errors.New("dial https://provider.internal:8443: connection refused"),
		fmt.Errorf("%w: no provider is granted to any tier or tenant", inference.ErrUnavailable),
	} {
		// Use the same GraphQL serialization as the guard, but deliberately bypass
		// coarsening. This must expose both URLs and non-URL gate text.
		schema := gql.MustParseSchema(`type Query { probe: String! }`, &wireCodeRoot{err: identity(err)})
		raw, marshalErr := json.Marshal(schema.Exec(context.Background(), `{ probe }`, "", nil))
		require.NoError(t, marshalErr)
		assert.True(t, inferenceWireLeaks(raw), "control missed an uncoarsened error: %s", raw)
	}
}
