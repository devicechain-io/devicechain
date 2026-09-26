// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
)

// A name or a tier colour that is "none" reads back null — whether the column holds NULL,
// or the empty string a pod on an earlier release wrote during a rolling upgrade (the
// appended migrations convert the rows that existed when they ran; they cannot reach one
// written after). The counterweight in each case: a real value reads back exactly.
func TestALegacyEmptyNameOrColourReadsAsNull(t *testing.T) {
	empties := map[string]sql.NullString{"NULL": {}, "empty string": {String: "", Valid: true}}
	for name, stored := range empties {
		id := iam.Identity{FirstName: stored, LastName: stored}
		for field, got := range map[string]*string{
			"admin firstName":   (&AdminIdentityResolver{M: id}).FirstName(),
			"admin lastName":    (&AdminIdentityResolver{M: id}).LastName(),
			"current firstName": (&CurrentIdentityResolver{id: &id}).FirstName(),
			"current lastName":  (&CurrentIdentityResolver{id: &id}).LastName(),
			"tier color":        (&AdminTenantTierResolver{M: iam.TenantTier{Color: stored}}).Color(),
		} {
			if got != nil {
				t.Errorf("%s stored as %s read back %q, want null", field, name, *got)
			}
		}
	}

	ada := sql.NullString{String: "Ada", Valid: true}
	if got := (&AdminIdentityResolver{M: iam.Identity{FirstName: ada}}).FirstName(); got == nil || *got != "Ada" {
		t.Errorf("a first name read back %v, want Ada", got)
	}
	amber := sql.NullString{String: string(iam.TierColorAmber), Valid: true}
	if got := (&AdminTenantTierResolver{M: iam.TenantTier{Color: amber}}).Color(); got == nil || *got != string(iam.TierColorAmber) {
		t.Errorf("a tier colour read back %v, want amber", got)
	}
}
