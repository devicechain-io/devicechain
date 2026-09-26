// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"database/sql"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// 🔴 A CLEARED NAME IS NULL IN THE COLUMN, AND A NAME IS TRIMMED LIKE OTHER TEXT.
//
// first_name / last_name have always been nullable columns, but the model held them as a
// bare string, so every clear wrote the empty string and a name was stored with whatever
// whitespace it arrived with. The model now spells the NULL the column allows, and the names follow the
// platform's rule for nullable descriptive text: a value is trimmed, and "", whitespace or
// null clear it to NULL.
//
// The row is read with raw SQL, not through the model, so the assertion is about what
// the column holds rather than about how a Go type happens to render it.
func TestAClearedNameIsStoredAsNull(t *testing.T) {
	seed := func(t *testing.T) (*Manager, func(column string) sql.NullString) {
		m := newPartialUpdateManager(t, &iam.Identity{}, &iam.Role{}, &iam.Membership{})
		ctx := putest.TenantContext(profileTenant)()
		require.NoError(t, m.iam.CreateIdentity(ctx, &iam.Identity{
			Email: profileEmail, Enabled: true, PasswordHash: "unused-by-this-suite",
		}))
		require.NoError(t, m.db.Database.Exec(
			"UPDATE iam_identities SET first_name = 'Ada', last_name = 'Lovelace' WHERE email = ?",
			profileEmail).Error)
		read := func(column string) sql.NullString {
			var out sql.NullString
			require.NoError(t, m.db.Database.Raw(
				"SELECT "+column+" FROM iam_identities WHERE email = ?", profileEmail).Scan(&out).Error)
			return out
		}
		return m, read
	}

	for name, clear := range map[string]dcgraphql.OptionalString{
		"empty string":  dcgraphql.OptionalStringOf(""),
		"whitespace":    dcgraphql.OptionalStringOf("   "),
		"explicit null": dcgraphql.ClearedString(),
	} {
		t.Run(name, func(t *testing.T) {
			m, read := seed(t)
			ctx := putest.TenantContext(profileTenant)()
			_, err := m.UpdateProfile(ctx, profileEmail, &ProfileUpdateRequest{FirstName: clear})
			require.NoError(t, err)
			require.Equal(t, sql.NullString{}, read("first_name"),
				"a cleared first name must be NULL in the column, not an empty string")
			require.Equal(t, sql.NullString{String: "Lovelace", Valid: true}, read("last_name"),
				"clearing one name changed the other")
		})
	}

	t.Run("a value is trimmed", func(t *testing.T) {
		m, read := seed(t)
		ctx := putest.TenantContext(profileTenant)()
		_, err := m.UpdateProfile(ctx, profileEmail, &ProfileUpdateRequest{
			FirstName: dcgraphql.OptionalStringOf(" Augusta "),
		})
		require.NoError(t, err)
		require.Equal(t, sql.NullString{String: "Augusta", Valid: true}, read("first_name"))
	})
}
