// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"slices"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

var reservedIntentAuthorities = []string{string(auth.IntentRead), string(auth.IntentWrite)}

// Every enabled member receives the viewer baseline. intent:read ends in ":read", so
// TestViewerAuthoritiesAreReadOnly would let it in without comment; reading a tenant's
// intents is a decision a role makes, so the absence has its own test.
func TestIntentAuthoritiesAreNotInTheViewerBaseline(t *testing.T) {
	for _, a := range reservedIntentAuthorities {
		if slices.Contains(viewerAuthorities, a) {
			t.Errorf("the viewer baseline must not grant %s", a)
		}
		if slices.Contains(effectiveAuthorities(nil, false), a) {
			t.Errorf("a roleless member must not receive %s", a)
		}
	}
}

// Shipping vocabulary must not opt any built-in role in. Inspect the real startup seed
// and its replay; wildcard administrator roles already satisfy both without naming them.
func TestIntentAuthoritiesAreNotSeededIntoAnyBuiltInRole(t *testing.T) {
	f := newSeedFixture(t)
	ctx := context.Background()
	for _, phase := range []string{"first startup", "restart"} {
		t.Run(phase, func(t *testing.T) {
			require.NoError(t, f.manager("seed-password").Initialize(ctx, &stubRefreshKV{}, nil))
			roles, err := iam.NewStore(f.rdbm).ListCatalogueRoles(ctx, nil)
			require.NoError(t, err)
			require.Len(t, roles, 3, "inspect the superuser, tenant-admin and viewer seeds")
			for _, role := range roles {
				for _, a := range reservedIntentAuthorities {
					require.NotContains(t, role.Authorities, a,
						"built-in role %s must not explicitly seed %s", role.Token, a)
				}
			}
		})
	}
}
