// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// resolveTenantGrant needs nothing from the Manager but its store, so it is built
// directly rather than through the full identity service (issuer, signing keys, KV) —
// none of which participates in the decision under test.
func newGrantTestManager(t *testing.T) (*Manager, *iam.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&iam.TenantTier{}, &iam.Tenant{}))
	store := iam.NewStore(&rdb.RdbManager{Database: db})
	return &Manager{iam: store}, store
}

func seedGrantTenant(t *testing.T, store *iam.Store, token string, state iam.PurgeState, enabled bool) {
	t.Helper()
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(ctx, tier))
	now := time.Now().UTC()
	tenant := &iam.Tenant{Token: token, Enabled: enabled, TierID: tier.ID, PurgeState: state}
	if state.Deleted() {
		tenant.PurgeEpoch = &now
	}
	require.NoError(t, store.CreateTenant(ctx, tenant))

	// 🔴 Enabled must be written AFTER the insert, and the reason is a gorm trap worth
	// stating: the column carries `default:true`, and gorm omits a zero-valued field on
	// insert so the database default wins — so `Enabled: false` above silently produces
	// an ENABLED row. That is not cosmetic here. It made the two refusal tests below run
	// the identical scenario, which a mutation swapping the lifecycle check for an
	// `!Enabled` check exposed by failing both of them instead of only one.
	require.NoError(t, store.SetTenantEnabled(ctx, tenant, enabled))
	reloaded, err := store.TenantByToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, enabled, reloaded.Enabled, "the seed did not produce the enabled state it claims")
}

// A deleted tenant admits no ordinary member, whatever its enabled flag says.
func TestResolveTenantGrantRefusesAPurgingTenant(t *testing.T) {
	m, store := newGrantTestManager(t)
	seedGrantTenant(t, store, "acme", iam.PurgePurging, false)

	mem := &iam.Membership{Enabled: true}
	_, _, err := m.resolveTenantGrant(context.Background(), "acme", mem, false)
	require.ErrorIs(t, err, errTenantAccessDenied)
}

// 🔴 THE POINT OF CHECKING THE LIFECYCLE RATHER THAN `Enabled`.
//
// Both are set at the cut, but only one of them is permanent: `enabled` is an
// operator-facing toggle with its own admin mutation, so a tenant flipped back on
// mid-purge would readmit its members to data being erased underneath them — and it
// would do so through a path that looks, in the admin UI, like an ordinary re-enable.
// This is the test that would fail if the check were moved onto `Enabled`.
func TestResolveTenantGrantRefusesAPurgingTenantEvenWhenReEnabled(t *testing.T) {
	m, store := newGrantTestManager(t)
	seedGrantTenant(t, store, "acme", iam.PurgePurging, true) // purging, but enabled again

	mem := &iam.Membership{Enabled: true}
	_, _, err := m.resolveTenantGrant(context.Background(), "acme", mem, false)
	require.ErrorIs(t, err, errTenantAccessDenied,
		"re-enabling a purging tenant must not readmit its members")
}

// A superuser still breaks glass into a purging tenant. Stated as a decision rather
// than left as an accident (ADR-077): operating a stuck purge is what break-glass is
// for, and the entry is audited as actingAsSuperuser. The cost — a break-glass session
// can write rows into a tenant an area has already swept — is what makes slice 2's
// fences epoch-bounded rather than "swept once".
func TestResolveTenantGrantAllowsSuperuserBreakGlassIntoAPurgingTenant(t *testing.T) {
	m, store := newGrantTestManager(t)
	seedGrantTenant(t, store, "acme", iam.PurgePurging, false)

	_, authorities, err := m.resolveTenantGrant(context.Background(), "acme", nil, true)
	require.NoError(t, err)
	require.Equal(t, []string{string(auth.AuthorityAll)}, authorities)
}

// 🔴 THE ASSUMPTION THE ENTIRE DEVICE-PLANE GATE RESTS ON (ADR-077 slice 1b).
//
// device-management, event-sources and command-delivery read a tenant's purgeState from
// tenantGovernance over a service token, and that query resolves through CurrentTenant.
// If CurrentTenant filtered the lifecycle the way resolveTenantGrant does — which is the
// obvious thing to do, and which the refusals above might invite someone to add — then
// the one query those services ask would fail for exactly the tenants it exists to
// report on, and every gate downstream would fail open forever while looking correct.
//
// So the asymmetry is deliberate and this is where it is pinned: the grant path refuses
// a purging tenant, the READ path still describes it.
func TestCurrentTenantStillAnswersForAPurgingTenant(t *testing.T) {
	m, store := newGrantTestManager(t)
	seedGrantTenant(t, store, "acme", iam.PurgePurging, false)

	got, err := m.CurrentTenant(context.Background(), "acme")
	require.NoError(t, err, "the lifecycle read must not be filtered by the lifecycle")
	require.Equal(t, iam.PurgePurging, got.PurgeState)
}

// The negative control: an ACTIVE tenant still grants normally, so the refusals above
// are about the lifecycle rather than a grant path that has stopped working.
func TestResolveTenantGrantStillAdmitsAnActiveTenant(t *testing.T) {
	m, store := newGrantTestManager(t)
	seedGrantTenant(t, store, "acme", iam.PurgeActive, true)

	mem := &iam.Membership{Enabled: true}
	_, _, err := m.resolveTenantGrant(context.Background(), "acme", mem, false)
	require.NoError(t, err)
}
