// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-mcp/server"
	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type skillBearer string

func (b skillBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func TestSkillReadMetricsThroughServiceRoutes(t *testing.T) {
	previous := Microservice
	t.Cleanup(func() { Microservice = previous })
	reg := prometheus.NewRegistry()
	Microservice = &core.Microservice{FunctionalArea: "mcp"}
	Microservice.UseMetricsRegistry(reg)
	key, err := coreauth.GenerateKeyPair()
	require.NoError(t, err)
	validator := coreauth.NewValidator(&key.PublicKey)
	registerHttpRoutes(testResource, testIssuer, func() *coreauth.Validator { return validator },
		server.NewGraphQLClient(), server.DefaultLimits())
	ts := httptest.NewServer(Microservice.Mux())
	t.Cleanup(ts.Close)
	issuer := coreauth.NewIssuer(key, testIssuer, 0, 0)
	token, err := issuer.IssueOAuthAccess("acme", "a@example.com", []string{"viewer"}, nil,
		coreauth.ScopeReadOnly, []string{testResource}, false, "metrics-test", "j-metrics")
	require.NoError(t, err)
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "metrics-test", Version: "1"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: skillBearer(token.Token)},
			MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	want := map[[2]string]float64{}
	check := func() {
		t.Helper()
		families, err := reg.Gather()
		require.NoError(t, err)
		got := map[[2]string]float64{}
		for _, family := range families {
			if family.GetName() != "devicechain_mcp_skill_reads_total" {
				continue
			}
			for _, metric := range family.Metric {
				require.Len(t, metric.Label, 2, "only skill and kind labels are permitted")
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				require.Contains(t, labels, "skill")
				require.Contains(t, labels, "kind")
				got[[2]string{labels["skill"], labels["kind"]}] = metric.GetCounter().GetValue()
			}
		}
		require.Equal(t, want, got)
	}
	check()
	_, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "unknown-skill"})
	require.Error(t, err)
	_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://unknown-skill/SKILL.md"})
	require.Error(t, err)
	check()
	prompts, err := cs.ListPrompts(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, prompts.Prompts)
	check()
	for _, prompt := range prompts.Prompts {
		for range 2 {
			_, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: prompt.Name})
			require.NoError(t, err)
			want[[2]string{prompt.Name, "prompt"}]++
			check()
		}
	}
	resources, err := cs.ListResources(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, resources.Resources)
	check()
	for _, resource := range resources.Resources {
		_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: resource.URI})
		require.NoError(t, err)
		// The listed name is <embedded skill>/<relative path>.
		name, _, _ := strings.Cut(resource.Name, "/")
		want[[2]string{name, "resource"}]++
		check()
	}
	_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://alarm-storm-triage/missing.md"})
	require.Error(t, err)
	_, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "another-unknown-skill"})
	require.Error(t, err)
	check()
}
