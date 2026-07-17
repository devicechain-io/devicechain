// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantGovernanceExposesTheTierToken is the user-management half of a CROSS-SERVICE
// wire contract, and the half that is easy to break without noticing.
//
// ai-inference reads `tenantGovernance { aiExternalEnabled tierToken }` over a service
// token and joins tierToken against its own grant tables to decide which AI models a
// tenant may use (ADR-065 decision 10). Renaming or dropping this field takes the NL
// authoring door down for every tenant, instance-wide, at runtime.
//
// It does NOT do so silently — this comment used to claim it would, and that was wrong.
// graph-gophers validates the selection, so the consumer's query errors outright rather
// than decoding a zero value, and the consumer surfaces that as ErrUnavailable with a
// log line. The reason to pin the name here is simpler and better: an instance-wide
// outage that announces itself is still an instance-wide outage, and this test costs a
// millisecond to turn it into a red build instead.
//
// The same string is pinned on the consuming end
// (inference.TestFactsQuerySelectsTheContractFields). Two tests, one contract, both cheap.
func TestTenantGovernanceExposesTheTierToken(t *testing.T) {
	r := &TenantGovernanceResolver{t: &iam.Tenant{
		Tier: &iam.TenantTier{Token: iam.TierGoldToken},
	}}
	assert.Equal(t, iam.TierGoldToken, r.TierToken())
}

// TestTierTokenIsServedUnderThatExactName reads the schema rather than the resolver, so
// it fails if the SDL and the Go method ever drift apart (graph-gophers binds by
// reflection, so a mismatch is a startup panic, but a rename that updates BOTH would
// pass every local test and silently break the consumer).
func TestTierTokenIsServedUnderThatExactName(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "TenantGovernance") { fields { name type { kind } } } }`, "", nil)
	require.Empty(t, res.Errors)

	var out struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Type struct {
					Kind string `json:"kind"`
				} `json:"type"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))

	var found bool
	for _, f := range out.Type.Fields {
		if f.Name == "tierToken" {
			found = true
			assert.Equal(t, "NON_NULL", f.Type.Kind,
				"tierToken is non-null: the tier is a NOT NULL FK on the tenant row (ADR-065 decision 3)")
		}
	}
	assert.True(t, found,
		"ai-inference selects tierToken by this exact name to resolve a tenant's AI model menu (ADR-065)")
}

// TestTierTokenIsEmptyRatherThanAPanicWithoutATier. The tier is a NOT NULL FK and the
// tenant is loaded with Preload("Tier"), so a nil tier should be unreachable. If that
// ever stops holding, an empty token is the fail-closed answer — it resolves to an empty
// menu at the consumer, whereas a panic would take down the query every enforcing
// service depends on for its ceilings.
func TestTierTokenIsEmptyRatherThanAPanicWithoutATier(t *testing.T) {
	r := &TenantGovernanceResolver{t: &iam.Tenant{}}
	assert.Equal(t, "", r.TierToken())
}
