// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/require"
)

// 🔴 AN EXPLICIT Enabled=false ON CREATE MUST BE STORED. gorm omits a bool's zero value
// from an INSERT when the column has a DEFAULT, so `default:true` on these models turned
// false into true. One row per model with Enabled left false, read back with raw SQL.
func TestCreateStoresExplicitEnabledFalse(t *testing.T) {
	db := putest.NewSQLiteDB(t, &Identity{}, &Role{}, &Membership{}, &Tenant{}, &TenantTier{}, &OAuthClient{})
	s := NewStore(&rdb.RdbManager{Database: db})
	sys := s.sys(context.Background())

	require.NoError(t, sys.Create(&Identity{Email: "a@example.invalid", PasswordHash: "x"}).Error)
	require.NoError(t, sys.Create(&Membership{IdentityID: 1, TenantId: "t1"}).Error)
	require.NoError(t, sys.Create(&Tenant{Token: "t1"}).Error)
	require.NoError(t, sys.Create(&OAuthClient{ClientId: "c1"}).Error)

	for _, table := range []string{"iam_identities", "iam_memberships", "iam_tenants", "iam_oauth_clients"} {
		var n int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM "+table+" WHERE enabled = ?", false).Scan(&n).Error)
		require.EqualValues(t, 1, n, "%s: an explicit false was swallowed by the column default", table)
	}
}
