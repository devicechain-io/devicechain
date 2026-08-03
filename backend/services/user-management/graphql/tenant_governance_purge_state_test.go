// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The user-management half of a CROSS-SERVICE wire contract (ADR-077 slice 1b).
//
// core/governance's TenantLifecycleResolver selects `tenantGovernance { purgeState }` by
// that literal string, and three services gate their device plane on the answer: device
// connect-auth, ingest auto-registration and command dispatch. Renaming or dropping the
// field does not silently zero the gates — graph-gophers validates the selection, so the
// consumer's query errors and its fail-open serves "active" — but that failure mode is
// precisely the one worth a red build instead: every gate in the instance quietly
// reverts to letting deleted tenants through, and nothing but a log line says so.
//
// The consuming end pins the same string in core/governance/lifecycle_resolver.go.
func TestPurgeStateIsServedUnderThatExactName(t *testing.T) {
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
		if f.Name == "purgeState" {
			found = true
			assert.Equal(t, "NON_NULL", f.Type.Kind,
				"purgeState is non-null: a tenant is always in exactly one lifecycle state, "+
					"and there is nothing above it to inherit a null from")
		}
	}
	assert.True(t, found,
		"core/governance selects purgeState by this exact name to gate the device plane (ADR-077)")
}

// The resolver reports the state verbatim, including the one that matters. A resolver
// that answered "active" for a purging tenant would leave every gate downstream open
// while the schema check above still passed.
func TestPurgeStateResolvesTheTenantsActualState(t *testing.T) {
	for _, state := range []iam.PurgeState{iam.PurgeActive, iam.PurgePurging} {
		r := &TenantGovernanceResolver{t: &iam.Tenant{PurgeState: state}}
		assert.Equal(t, string(state), r.PurgeState())
	}
}

// 🔴 The two halves of the contract, compared to EACH OTHER rather than each to its own
// literal.
//
// core/governance cannot import this module, so it carries its own "active" constant and
// decides every gate on the instance by comparing the served state against it. The first
// version of this test asserted `iam.PurgeActive == "active"` and left a comment claiming
// it pinned core's constant — which it did not: changing governance.LifecycleActive to
// anything else left the whole suite green while, in production, every tenant would read
// DELETED once cached and the instance would refuse every device connect, every ingest
// and every command. An assertion message is not an assertion.
//
// The dependency runs one way, so this is the side that can make the comparison.
func TestLifecycleActiveMatchesTheStateCoreCompares(t *testing.T) {
	assert.Equal(t, string(iam.PurgeActive), governance.LifecycleActive,
		"core/governance decides every device-plane gate by comparing the served purgeState "+
			"against LifecycleActive; if they drift, every tenant reads deleted instance-wide")
}

// And the served value is stable, so a rename here is caught even before it reaches core.
func TestLifecycleStateWireValuesAreStable(t *testing.T) {
	assert.Equal(t, "active", string(iam.PurgeActive))
	assert.Equal(t, "purging", string(iam.PurgePurging))
}
