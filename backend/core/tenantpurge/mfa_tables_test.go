// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The second-factor tables user-management gained. Their DDL is mirrored here by hand, because
// core cannot import a service's migration; the REAL tables are held to the same classification
// by the coverage gate (hack/migration-diff.sh coverage). This pins the part that gate cannot say
// in words: all three are identity-global, so a tenant's deletion leaves them alone -- deleting
// one tenant must not remove a person's second factor.
func TestMfaTablesAreIdentityGlobalAndLeftAloneByATenantPurge(t *testing.T) {
	const s = "user-management"
	cols := []column{
		col(s, "iam_identities", "id", "bigint"),
		col(s, "iam_mfa_factors", "id", "bigint"),
		col(s, "iam_mfa_factors", "identity_id", "bigint"),
		col(s, "iam_mfa_source_failures", "factor_id", "bigint"),
		col(s, "iam_mfa_recovery_codes", "id", "bigint"),
		col(s, "iam_mfa_recovery_codes", "identity_id", "bigint"),
	}
	fks := []constraint{
		fk(s, "iam_mfa_factors", "identity_id", s, "iam_identities", "id"),
		fk(s, "iam_mfa_source_failures", "factor_id", s, "iam_mfa_factors", "id"),
		fk(s, "iam_mfa_recovery_codes", "identity_id", s, "iam_identities", "id"),
	}
	plan := planOf(t, cols, fks) // planOf also fails if ANY table is unclassified
	for _, name := range []string{"iam_mfa_factors", "iam_mfa_source_failures", "iam_mfa_recovery_codes"} {
		e := classOf(t, plan, s, name)
		assert.Equalf(t, ClassExempt, e.Class, "%s holds no single tenant's data", name)
		assert.NotEmptyf(t, e.Reason, "%s: an exemption without a reason has not earned its place", name)
	}
}
