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
