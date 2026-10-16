// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"github.com/stretchr/testify/require"
)

// TestIsolationSweep calls every query, mutation and subscription update-management serves
// as acme's member, aimed at globex's device, and fails on any answer that carries globex's
// assignment or tells globex's device apart from a missing one (ADR-081). It runs on a real
// PostgreSQL because the scope predicate is an array overlap sqlite cannot evaluate. Its
// negative control is the same queries as the unscoped operator, which must surface globex.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run TestIsolationSweep -v
func TestIsolationSweep(t *testing.T) {
	p := pgPlane(t)
	p.available("fw-1", []byte("image"))
	op := p.ctx("acme", auth.ArtifactRead, auth.ArtifactWrite)
	assignment := map[string]string{}
	for _, d := range []string{"dev-acme", "dev-globex"} {
		resp := p.exec(op, assignMutation, map[string]any{"d": d, "a": "fw-1"})
		require.Empty(t, resp.Errors, d)
		var got struct{ AssignArtifact struct{ Token string } }
		require.NoError(t, json.Unmarshal(resp.Data, &got))
		assignment[d] = got.AssignArtifact.Token
	}

	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", AllowUnverifiedArtifacts: true}),
		Scoped:   p.member("customer:acme"),
		Unscoped: op,
		// The probe is globex's device, so a by-device filter is aimed at it; globex's
		// assignment token is the marker, since the probe itself is substituted away
		// before an answer is read.
		Probe:  "dev-globex",
		Hidden: []string{assignment["dev-globex"], "customer:globex"},
	})
	counts := map[string]int{}
	for _, o := range out {
		switch {
		case o.Data:
			counts["data"]++
		case len(o.Codes) > 0:
			counts[o.Codes[0]]++
		default:
			counts["silent"]++
		}
	}
	t.Logf("swept %d operations as acme's member: %v", len(out), counts)
	// The sweep reached the opted-in list. It selects every field, the tenant's staff among
	// them (actor, provenance, signatures), which a scoped member is refused at the field, so
	// its answer over acme's own assignment is that refusal rather than data;
	// TestScopedAssignmentsReturnOnlyAcme reads the rows themselves. The by-token read,
	// aimed at a token naming nothing of acme's, answers null as for a missing one.
	if o, ok := isolationsweep.Find(out, "query updateAssignments"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query updateAssignments: %+v (reached=%v), want the staff fields refused to acme's member", o, ok)
	}
	if o, ok := isolationsweep.Find(out, "query updateAssignment"); !ok || !o.Data {
		t.Errorf("query updateAssignment: %+v (reached=%v), want null data for acme's member", o, ok)
	}
}
