// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCustomerTypeExpectedUpdatedAtRace(t *testing.T) {
	api, ctx, original, _ := customerTypePartialFixture(t)
	expected := original.UpdatedAt.Format(time.RFC3339Nano)
	fired := false
	const callback = "test:customer_type_cas_race"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "customer_types" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&CustomerType{}).Where("id = ?", original.ID).
			Updates(map[string]any{"name": "winner", "updated_at": original.UpdatedAt.Add(time.Second)}).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
	updated, err := api.UpdateCustomerType(ctx, original.Token, &expected, &CustomerTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("loser"),
	})
	require.True(t, fired)
	require.Nil(t, updated)
	require.ErrorIs(t, err, ErrCustomerTypeConflict)
	rows, err := api.CustomerTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "CustomerType", rows, err)
	require.Equal(t, "winner", nullStr(stored.Name))
	require.Equal(t, original.UpdatedAt.Add(time.Second), stored.UpdatedAt)
}

func TestCustomerTypeExpectedUpdatedAtAndLegacyVersion(t *testing.T) {
	api, ctx, original, _ := customerTypePartialFixture(t)
	api.RDB.Database.NowFunc = func() time.Time { return original.UpdatedAt }
	expected := original.UpdatedAt.Format(time.RFC3339Nano)
	updated, err := api.UpdateCustomerType(ctx, original.Token, &expected, &CustomerTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("winner"),
	})
	require.NoError(t, err)
	require.True(t, updated.UpdatedAt.After(original.UpdatedAt))
	winnerVersion := updated.UpdatedAt
	for _, stale := range []string{expected, "", "invalid"} {
		for _, request := range []*CustomerTypeUpdateRequest{{Name: dcgraphql.OptionalStringOf("loser")}, {}} {
			result, err := api.UpdateCustomerType(ctx, original.Token, &stale, request)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrCustomerTypeConflict)
			rows, err := api.CustomerTypesByToken(ctx, []string{original.Token})
			stored := requireOne(t, "CustomerType", rows, err)
			require.Equal(t, "winner", nullStr(stored.Name))
			require.Equal(t, winnerVersion, stored.UpdatedAt)
		}
	}
	updated, err = api.UpdateCustomerType(ctx, original.Token, nil, &CustomerTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("legacy"),
	})
	require.NoError(t, err)
	require.Equal(t, "legacy", nullStr(updated.Name))
	require.True(t, updated.UpdatedAt.After(winnerVersion), "legacy writes must advance even with a frozen clock")
	expected = winnerVersion.Format(time.RFC3339Nano)
	_, err = api.UpdateCustomerType(ctx, original.Token, &expected, &CustomerTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("stale"),
	})
	require.ErrorIs(t, err, ErrCustomerTypeConflict)
}
