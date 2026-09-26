// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"database/sql"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// THIS SERVICE'S SELF-SERVICE HALF OF THE PARTIAL-UPDATE HARNESS.
//
// user-management's five converted mutations sit behind two application services, and
// the harness's Family is parameterised by the service type so a family's closures
// compile against the real methods. updateProfile is *identity.Manager's, so it gets its
// own Suite and its own exhaustiveness guard rather than being folded into the admin
// package's — a merged registry would have to erase the receiver type, and the guard
// reflects over exactly one service type per call, so one of the two update surfaces
// would go unenumerated.
//
// The properties themselves are core's; read rdb/partialupdatetest/harness.go for what
// each one catches.

const (
	// profileEmail is the identity the family edits. updateProfile is SELF-scoped — the
	// row is named by the caller's own token subject — so the email is what the harness
	// addresses by, standing where a token stands for every other family.
	profileEmail = "ada@example.invalid"
	// profileTenant is supplied for the same reason the admin suite supplies one: the
	// iam control-plane tables are not tenant-scoped, but the fail-closed tenant-scope
	// callback is registered exactly as production registers it, and a suite that ran
	// without a tenant would be relying on that staying true.
	profileTenant = "acme"
)

// newPartialUpdateManager builds a Manager over a throwaway database with only the parts
// UpdateProfile actually touches wired.
//
// 🔴 THE SIGNING KEY, THE LOCK AND THE KV ARE DELIBERATELY ABSENT. A Manager built
// through NewManager needs a Microservice, a distributed lock and a live NATS KV before
// Initialize will run, none of which UpdateProfile reads — it resolves an identity by
// email and writes two columns. Constructing the whole thing would make this suite need
// an embedded JetStream server to certify a name change, and a fixture that expensive is
// one that gets deleted.
func newPartialUpdateManager(t *testing.T, tables ...any) *Manager {
	t.Helper()
	db := putest.NewSQLiteDB(t, tables...)
	return &Manager{iam: iam.NewStore(&rdb.RdbManager{Database: db}), db: &rdb.RdbManager{Database: db}}
}

// TestPartialUpdate drives every property over the profile family.
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Manager]{
		NewApi:   newPartialUpdateManager,
		Context:  putest.TenantContext(profileTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe goes through a ROLE rather than an identity, and that is
		// forced rather than chosen: the token-grammar callback fires on any entity with a
		// Token column, and iam.Identity has none — it is keyed by email. Probing with an
		// identity would therefore assert nothing about the grammar and the property would
		// pass for a fixture that had never registered it.
		CreateWithToken: func(t *testing.T, m *Manager, ctx context.Context, token string) error {
			return m.iam.CreateRole(ctx, &iam.Role{Scope: iam.ScopeSystem, Token: token})
		},
		StrictnessTables: []any{&iam.Role{}},
		ValidToken:       "a-valid_token1",
	})
}

func partialUpdateFamilies() []putest.Family[*Manager] {
	return []putest.Family[*Manager]{profileFamily()}
}

func profileFamily() putest.Family[*Manager] {
	return putest.Family[*Manager]{
		Name:    "profile",
		Token:   profileEmail,
		Migrate: []any{&iam.Identity{}, &iam.Role{}, &iam.Membership{}},
		Seed: func(t *testing.T, m *Manager, ctx context.Context) {
			if err := m.iam.CreateIdentity(ctx, &iam.Identity{
				Email: profileEmail, FirstName: nullStr("Ada"), LastName: nullStr("Lovelace"),
				Enabled: true, PasswordHash: "unused-by-this-suite",
			}); err != nil {
				t.Fatalf("seed identity: %v", err)
			}
		},
		Read: func(t *testing.T, m *Manager, ctx context.Context) map[string]string {
			id, err := m.iam.IdentityByEmail(ctx, profileEmail)
			if err != nil {
				t.Fatalf("reload identity: %v", err)
			}
			return map[string]string{
				"firstName": putest.NullString(id.FirstName),
				"lastName":  putest.NullString(id.LastName),
			}
		},
		NewRequest: func() any { return new(ProfileUpdateRequest) },
		Update: func(m *Manager, ctx context.Context, email string, req any) error {
			_, err := m.UpdateProfile(ctx, email, req.(*ProfileUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			// Ordinary nullable text: a clear reads back as NULL. The columns have always
			// been nullable, and the model now holds sql.NullString, so NullMarker is the
			// honest cleared reading rather than the "" a bare-string model could only write.
			putest.OptionalStringField("firstName", "Ada", "Augusta",
				func(r *ProfileUpdateRequest) *dcgraphql.OptionalString { return &r.FirstName }),
			putest.OptionalStringField("lastName", "Lovelace", "Byron",
				func(r *ProfileUpdateRequest) *dcgraphql.OptionalString { return &r.LastName }),
		},
	}
}

// 🔴 THE EXHAUSTIVENESS CHECK OVER *Manager's UPDATE SURFACE.
//
// 🔴 THIS GUARD IS WHY updateProfile HAS AN INPUT TYPE AT ALL. Before the conversion the
// mutation took two loose nullable arguments — `UpdateProfile(ctx, email, firstName,
// lastName *string)` — which the guard reports by NAME rather than skipping:
//
//	takes no struct parameter, so there is no update input for this rule to certify …
//	Either it is not an entity update — name it in NotAnEntityUpdate with the reason —
//	or its arguments are loose scalars that need collecting into a dedicated
//	*UpdateRequest
//
// It is an entity update; it simply never had an input object. Collecting the two
// arguments into ProfileUpdateRequest is what makes the mechanism visible, and the
// behaviour is unchanged — see the manager's own header for what each state does.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Manager]{
		Families: partialUpdateFamilies(),
		// Nothing on this service is still on the full-replace shape, and nothing on it is
		// outside the rule. Both maps are written out empty rather than omitted, so "the
		// residual is zero" is a statement rather than an absence.
		Exempt:            map[string]string{},
		NotAnEntityUpdate: map[string]string{},
		// The TOTAL number of Update* methods on the Manager, which is one. A floor of one
		// is weak by construction — it is the honest number here, not a floor lowered to
		// make a walk pass, and the day this service gains a second update the guard fails
		// until someone raises it and looks at what was added.
		MinUpdateMethods: 1,
	})
}

// TestUpdateProfileClearsANameToNull pins the clear a display name has always offered:
// "" clears it, and so does an explicit null — two spellings of one request.
//
// The clear is the part a mechanical conversion would break — ApplyToRequired refuses a
// blank as "a null spelled differently", which is right for a vocabulary column and wrong
// for a display name — so it is asserted here rather than left to the fold's own
// reasoning. What the clear WRITES is NULL: see TestAClearedNameIsStoredAsNull for the
// column itself.
func TestUpdateProfileClearsANameToNull(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request *ProfileUpdateRequest
	}{
		{"empty string", &ProfileUpdateRequest{FirstName: dcgraphql.OptionalStringOf("")}},
		{"explicit null", &ProfileUpdateRequest{FirstName: dcgraphql.ClearedString()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newPartialUpdateManager(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{})
			ctx := putest.TenantContext(profileTenant)()
			require.NoError(t, m.iam.CreateIdentity(ctx, &iam.Identity{
				Email: profileEmail, FirstName: nullStr("Ada"), LastName: nullStr("Lovelace"),
				Enabled: true, PasswordHash: "unused-by-this-suite",
			}))

			updated, err := m.UpdateProfile(ctx, profileEmail, tc.request)
			require.NoError(t, err)
			require.Equal(t, sql.NullString{}, updated.FirstName)
			require.Equal(t, nullStr("Lovelace"), updated.LastName, "clearing one name cleared the other")

			// From the database, not from the returned struct: a fold that mutated its
			// in-memory copy and wrote nothing would satisfy the assertion above.
			reloaded, err := m.iam.IdentityByEmail(ctx, profileEmail)
			require.NoError(t, err)
			require.Equal(t, sql.NullString{}, reloaded.FirstName)
			require.Equal(t, nullStr("Lovelace"), reloaded.LastName)
		})
	}
}

// TestUpdateProfileWritesOnlyTheColumnsItWasGiven pins the shape UpdateProfile has always
// had and that the conversion must not have widened: it builds a column MAP, one key per
// field the caller actually mentioned, so an update naming neither name touches nothing.
//
// A version that always wrote both columns would pass every property in the harness —
// rewriting a column with the value just read back is invisible — and would still be
// wrong, because it turns a no-op edit into a write that races any concurrent one.
func TestUpdateProfileWritesOnlyTheColumnsItWasGiven(t *testing.T) {
	m := newPartialUpdateManager(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{})
	ctx := putest.TenantContext(profileTenant)()
	require.NoError(t, m.iam.CreateIdentity(ctx, &iam.Identity{
		Email: profileEmail, FirstName: nullStr("Ada"), LastName: nullStr("Lovelace"),
		Enabled: true, PasswordHash: "the-hash",
	}))

	_, err := m.UpdateProfile(ctx, profileEmail, &ProfileUpdateRequest{
		FirstName: dcgraphql.OptionalStringOf("Augusta"),
	})
	require.NoError(t, err)

	reloaded, err := m.iam.IdentityByEmail(ctx, profileEmail)
	require.NoError(t, err)
	require.Equal(t, nullStr("Augusta"), reloaded.FirstName)
	require.Equal(t, nullStr("Lovelace"), reloaded.LastName)
	// The columns this mutation must never touch, whatever it is sent.
	require.Equal(t, profileEmail, reloaded.Email)
	require.Equal(t, "the-hash", reloaded.PasswordHash)
	require.True(t, reloaded.Enabled)
}

func nullStr(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
