// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestStore spins up an in-memory sqlite database with the core callbacks
// registered (as production does) and the iam_tenants table migrated. Tenants are
// instance-global (not tenant-scoped), reached through the system context, so this
// exercises the real UpdateTenant Select path.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&Tenant{}))
	return NewStore(&rdb.RdbManager{Database: db})
}

// TestUpdateTenantRoundTripsAiConsent pins the write-side fail-closed invariant
// (ADR-056 §6): an operator must be able to GRANT and then REVOKE external-AI
// consent via UpdateTenant. Because UpdateTenant uses a column allowlist (not a
// full-row Save), a revocation is only persisted if AiExternalEnabled is in that
// Select — this test is the guard against a stale `true` surviving a revoke (a
// fail-OPEN bug that would keep routing a withdrawn tenant's data externally).
func TestUpdateTenantRoundTripsAiConsent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tru, fls := true, false

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme"}))

	reload := func() *Tenant {
		got, err := s.TenantByToken(ctx, "acme")
		require.NoError(t, err)
		return got
	}

	// A freshly-created tenant is not opted in.
	require.Nil(t, reload().AiExternalEnabled)

	// Grant consent.
	tt := reload()
	tt.AiExternalEnabled = &tru
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.NotNil(t, reload().AiExternalEnabled)
	require.True(t, *reload().AiExternalEnabled)

	// Revoke to explicit false — must persist (bool zero value is NOT skipped under
	// an explicit Select).
	tt = reload()
	tt.AiExternalEnabled = &fls
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.NotNil(t, reload().AiExternalEnabled)
	require.False(t, *reload().AiExternalEnabled)

	// Grant again, then revoke to nil — must clear to NULL, not leave a stale true.
	tt = reload()
	tt.AiExternalEnabled = &tru
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.True(t, *reload().AiExternalEnabled)

	tt = reload()
	tt.AiExternalEnabled = nil
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.Nil(t, reload().AiExternalEnabled)
}

// TestUpdateTenantRoundTripsAiGovernance pins the same write-side invariant for the
// AI-inference rate overrides (ADR-056 §6 / ADR-023). UpdateTenant's column allowlist
// is the hazard: a column missing from that Select is SILENTLY unwritable — the update
// reports success while the old value survives. For a consent flag that fails open; for
// a ceiling it means a tightened limit never takes effect and an operator reacting to a
// runaway tenant would watch their change do nothing.
//
// This asserts the round trip at the DB, not just that applyTo sets the struct fields.
func TestUpdateTenantRoundTripsAiGovernance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme"}))

	reload := func() *Tenant {
		got, err := s.TenantByToken(ctx, "acme")
		require.NoError(t, err)
		return got
	}

	// A freshly-created tenant declares no override (inherits the platform default).
	require.Nil(t, reload().AiInferenceRequestsPerMinute)
	require.Nil(t, reload().AiInferenceBurst)

	// Declare an override.
	rate, burst := 12.5, 4
	tt := reload()
	tt.AiInferenceRequestsPerMinute = &rate
	tt.AiInferenceBurst = &burst
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.NotNil(t, reload().AiInferenceRequestsPerMinute)
	require.Equal(t, 12.5, *reload().AiInferenceRequestsPerMinute)
	require.NotNil(t, reload().AiInferenceBurst)
	require.Equal(t, 4, *reload().AiInferenceBurst)

	// TIGHTEN it — the case an operator reaches for against a runaway tenant.
	tighter, tighterBurst := 2.0, 1
	tt = reload()
	tt.AiInferenceRequestsPerMinute = &tighter
	tt.AiInferenceBurst = &tighterBurst
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.Equal(t, 2.0, *reload().AiInferenceRequestsPerMinute)
	require.Equal(t, 1, *reload().AiInferenceBurst)

	// Clear back to nil — must write NULL (inherit the default), not leave the old
	// ceiling in place.
	tt = reload()
	tt.AiInferenceRequestsPerMinute = nil
	tt.AiInferenceBurst = nil
	require.NoError(t, s.UpdateTenant(ctx, tt))
	require.Nil(t, reload().AiInferenceRequestsPerMinute)
	require.Nil(t, reload().AiInferenceBurst)
}
