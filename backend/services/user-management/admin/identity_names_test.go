// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// An identity created with no name stores NULL, not the empty string, and a name is
// trimmed like every other nullable display text on the platform. Read with raw SQL, so
// the assertion is about the column rather than about a Go type's rendering.
func TestCreateIdentityStoresAnOmittedNameAsNull(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{})
	s := NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 300*time.Second, 12*time.Hour, nil)
	ctx := putest.TenantContext(partialUpdateTenant)()
	read := func(email, column string) sql.NullString {
		var out sql.NullString
		require.NoError(t, db.Raw(
			"SELECT "+column+" FROM iam_identities WHERE email = ?", email).Scan(&out).Error)
		return out
	}

	_, err := s.CreateIdentity(ctx, CreateIdentityInput{Email: "anon@example.invalid", Password: "pw", Enabled: true})
	require.NoError(t, err)
	require.Equal(t, sql.NullString{}, read("anon@example.invalid", "first_name"))
	require.Equal(t, sql.NullString{}, read("anon@example.invalid", "last_name"))

	_, err = s.CreateIdentity(ctx, CreateIdentityInput{
		Email: "ada@example.invalid", Password: "pw", Enabled: true,
		FirstName: strp(" Ada "), LastName: strp("   "),
	})
	require.NoError(t, err)
	require.Equal(t, sql.NullString{String: "Ada", Valid: true}, read("ada@example.invalid", "first_name"))
	require.Equal(t, sql.NullString{}, read("ada@example.invalid", "last_name"),
		"a whitespace-only name is no name")
}

// 🔴 THE TIER COLOUR IS VALIDATED AFTER IT IS FOLDED, so what is checked is exactly what
// is stored. " amber " is trimmed to a palette token and accepted; a value that is not a
// token is refused, on create and on update, and a refused update writes nothing.
func TestTierColorIsValidatedAfterFolding(t *testing.T) {
	s := newPartialUpdateService(t, &iam.TenantTier{}, &iam.Tenant{})
	ctx := putest.TenantContext(partialUpdateTenant)()

	created, err := s.CreateTenantTier(ctx, TierInput{Token: "gold", Color: strp(" amber ")})
	require.NoError(t, err)
	require.Equal(t, sql.NullString{String: string(iam.TierColorAmber), Valid: true}, created.Color)

	_, err = s.CreateTenantTier(ctx, TierInput{Token: "bad", Color: strp("not-a-colour")})
	require.True(t, errors.Is(err, ErrUnknownTierColor), "an unknown colour on create: got %v", err)

	_, err = s.CreateTenantTier(ctx, TierInput{Token: "plain"})
	require.NoError(t, err)
	plain, err := s.iam.TenantTierByToken(ctx, "plain")
	require.NoError(t, err)
	require.Equal(t, sql.NullString{}, plain.Color, "a tier created with no colour stores NULL")

	updated, err := s.UpdateTenantTier(ctx, "gold", &TierUpdateRequest{Color: dcgraphql.OptionalStringOf(" violet\t")})
	require.NoError(t, err)
	require.Equal(t, sql.NullString{String: string(iam.TierColorViolet), Valid: true}, updated.Color)

	_, err = s.UpdateTenantTier(ctx, "gold", &TierUpdateRequest{Color: dcgraphql.OptionalStringOf("not-a-colour")})
	require.True(t, errors.Is(err, ErrUnknownTierColor), "an unknown colour on update: got %v", err)
	reloaded, err := s.iam.TenantTierByToken(ctx, "gold")
	require.NoError(t, err)
	require.Equal(t, sql.NullString{String: string(iam.TierColorViolet), Valid: true}, reloaded.Color,
		"a refused update wrote the colour anyway")

	cleared, err := s.UpdateTenantTier(ctx, "gold", &TierUpdateRequest{Color: dcgraphql.OptionalStringOf("")})
	require.NoError(t, err)
	require.Equal(t, sql.NullString{}, cleared.Color, "\"\" clears the colour to NULL")
}
