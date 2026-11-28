// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSkillReadCounterHasOnlyEmbeddedNamesAndKinds(t *testing.T) {
	const name = "devicechain_mcp_skill_reads_total"
	reg := prometheus.NewRegistry()
	reads := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: name, Help: "Successful embedded MCP skill reads.",
	}, []string{"skill", "kind"})
	reg.MustRegister(reads)
	cs, _ := skillSession(t, "acme", coreauth.ScopeReadOnly, false, WithSkillReadCounter(reads))
	ctx := context.Background()
	want := map[[2]string]int{}
	check := func() {
		t.Helper()
		var expected strings.Builder
		if len(want) > 0 {
			fmt.Fprintf(&expected, "# HELP %s Successful embedded MCP skill reads.\n# TYPE %s counter\n", name, name)
		}
		for labels, count := range want {
			fmt.Fprintf(&expected, "%s{kind=%q,skill=%q} %d\n", name, labels[1], labels[0], count)
		}
		require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected.String()), name))
	}
	unknown := func() {
		t.Helper()
		_, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "unknown-skill"})
		require.Error(t, err)
		check()
		_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://unknown-skill/SKILL.md"})
		require.Error(t, err)
		check()
		_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://alarm-storm-triage/missing.md"})
		require.Error(t, err)
		check()
	}
	check()
	unknown()
	_, err := cs.ListPrompts(ctx, nil)
	require.NoError(t, err)
	_, err = cs.ListResources(ctx, nil)
	require.NoError(t, err)
	check()
	for _, skill := range platformSkills {
		for range 2 {
			_, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: skill.Name})
			require.NoError(t, err)
			want[[2]string{skill.Name, "prompt"}]++
			check()
		}
		for _, file := range skill.Files {
			_, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: file.URI})
			require.NoError(t, err)
			want[[2]string{skill.Name, "resource"}]++
			check()
		}
	}
	unknown()
}
