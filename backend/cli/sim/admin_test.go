// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"errors"
	"fmt"
	"testing"
)

// tolerateExists is what makes `dcctl sim create` idempotent, and it is a substring
// match on the server's error text — which means it decides, by reading prose, whether
// a failed create counts as success. This pins what it must NOT swallow.
//
// 🔴 The case that motivated it: ADR-077 made a deleted tenant's token permanently
// RESERVED, so `createTenant` at a used token is now a refusal rather than a
// collision. If that refusal were tolerated, dcctl would carry on and mint an identity
// and a tenant-admin membership against a tenant it does not own — handing a sim
// operator the previous tenant's devices and telemetry, which is precisely the
// disclosure ADR-077 exists to close, reached through the front door.
//
// user-management holds the other half of this coupling
// (admin.ErrTenantTokenReserved's wording, asserted in admin/tenant_purge_test.go).
// It is not imported here on purpose: dcctl does not depend on a service module, so
// the two sides are pinned independently and this test states what it is pinning.
func TestTolerateExistsSwallowsOnlyAnAlreadyExists(t *testing.T) {
	tolerated := []string{
		`pq: duplicate key value violates unique constraint "idx_iam_tenants_token"`,
		"tenant already exists",
		"ERROR: duplicate key",
		"UNIQUE constraint failed: iam_tenants.token",
		// The server's prose still wins when the caller's echoed input is irrelevant.
		`create tenant "sim-wl": tenant already exists`,
	}
	for _, msg := range tolerated {
		inner := errors.New(msg)
		if got := tolerateExists(fmt.Errorf("create tenant: %w", inner), inner); got != nil {
			t.Errorf("tolerateExists did NOT swallow an already-exists (%q): %v", msg, got)
		}
	}

	refusals := []string{
		// The ADR-077 reservation, in the shape user-management sends it.
		`create tenant "sim-wl": that tenant token is reserved: a tenant at this token was ` +
			`deleted. Tokens are never reused, because every functional area keys its rows on ` +
			`the token; pick a different one`,
		// Ordinary failures that must never read as success either.
		`unknown tier "platinum"`,
		"tenant does not exist",
		"connection refused",
		"permission denied",

		// 🔴 THE CASE THAT DEFEATED THE FIRST VERSION OF THIS TEST. The server echoes the
		// caller's identifiers back inside quotes, so a sim whose NAME contains a
		// tolerated phrase used to decide its own outcome: every one of these is a real
		// failure whose only tolerated word is in the caller's own input.
		`create tenant "sim-unique1": that tenant token is reserved; pick a different one`,
		`create tenant "sim-duplicate-check": connection refused`,
		`create identity "duplicate-name@sim.devicechain.local": permission denied`,
		`create tenant "sim-unique1": unknown tier "platinum"`,
	}
	for _, msg := range refusals {
		inner := errors.New(msg)
		got := tolerateExists(fmt.Errorf("create tenant: %w", inner), inner)
		if got == nil {
			t.Errorf("🔴 tolerateExists SWALLOWED a real refusal as success: %q", msg)
			continue
		}
		if !errors.Is(got, inner) {
			t.Errorf("the returned error dropped its cause for %q: %v", msg, got)
		}
	}

	// A nil inner error is success and must stay success — without this the loop above
	// would pass just as happily against a tolerateExists that rejected everything.
	if got := tolerateExists(errors.New("wrapped"), nil); got != nil {
		t.Errorf("tolerateExists turned a success into an error: %v", got)
	}
}
