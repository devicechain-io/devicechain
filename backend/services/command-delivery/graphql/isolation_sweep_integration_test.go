// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package graphql

import (
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"github.com/stretchr/testify/require"
)

// TestIsolationSweep calls every query, mutation and subscription command-delivery serves
// as acme's member, aimed at globex's device, and fails on any answer that carries globex's
// commands or tells globex's device apart from a missing one (ADR-081). It runs on a real
// PostgreSQL because the scope predicate is an array overlap sqlite cannot evaluate. Its
// negative control is the same queries as the unscoped operator, which must surface globex.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run TestIsolationSweep -v
func TestIsolationSweep(t *testing.T) {
	_, ctx := scopeFixture(t)
	op := asOperator(ctx)
	require.NotNil(t, enqueueTo(t, op, "c-acme", "acme-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-globex", "globex-1").CreateCommand.Command)
	// A command whose token is the probe too, so a by-token read is aimed at globex's
	// command as well as a by-device one at globex's device.
	require.NotNil(t, enqueueTo(t, op, "globex-1", "globex-1").CreateCommand.Command)

	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   asMember(ctx, acmeAnchor),
		Unscoped: op,
		Probe:    "globex-1",
		Hidden:   []string{"globex"},
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
	// The sweep reached the opted-in reads, and they answered data rather than a refusal.
	for _, name := range []string{"query commands", "query commandsByToken"} {
		o, ok := isolationsweep.Find(out, name)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", name, o, ok)
		}
	}
}
