// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Keep the storage guards observable independently of GraphQL argument forwarding.
func TestEntityExpectedUpdatedAtStorage(t *testing.T) {
	for _, tc := range []struct {
		entity   string
		table    string
		record   func() any
		conflict *rdb.StaleWriteError
		update   func(*Api, context.Context, *string, string) error
	}{
		{"Area", "areas", func() any { return &Area{TokenReference: rdb.TokenReference{Token: "record"}} }, ErrAreaConflict,
			func(api *Api, ctx context.Context, expected *string, name string) error {
				_, err := api.UpdateArea(ctx, "record", expected, &AreaUpdateRequest{Name: dcgraphql.OptionalStringOf(name)})
				return err
			}},
		{"Asset", "assets", func() any { return &Asset{TokenReference: rdb.TokenReference{Token: "record"}} }, ErrAssetConflict,
			func(api *Api, ctx context.Context, expected *string, name string) error {
				_, err := api.UpdateAsset(ctx, "record", expected, &AssetUpdateRequest{Name: dcgraphql.OptionalStringOf(name)})
				return err
			}},
		{"Customer", "customers", func() any { return &Customer{TokenReference: rdb.TokenReference{Token: "record"}} }, ErrCustomerConflict,
			func(api *Api, ctx context.Context, expected *string, name string) error {
				_, err := api.UpdateCustomer(ctx, "record", expected, &CustomerUpdateRequest{Name: dcgraphql.OptionalStringOf(name)})
				return err
			}},
		{"EntityGroup", "entity_groups", func() any {
			return &EntityGroup{TokenReference: rdb.TokenReference{Token: "record"}, MemberType: "device", MembershipMode: "static"}
		}, ErrEntityGroupConflict,
			func(api *Api, ctx context.Context, expected *string, name string) error {
				_, err := api.UpdateEntityGroup(ctx, "record", expected, &EntityGroupUpdateRequest{Name: dcgraphql.OptionalStringOf(name)})
				return err
			}},
	} {
		t.Run(tc.entity, func(t *testing.T) {
			api := newPartialUpdateApi(t, &Area{}, &AreaType{}, &Asset{}, &AssetType{}, &Customer{}, &CustomerType{}, &EntityGroup{})
			ctx := partialUpdateCtx()
			db := api.RDB.Database
			now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			db.NowFunc = func() time.Time { return now }
			require.NoError(t, api.RDB.DB(ctx).Create(tc.record()).Error)
			read := func() struct {
				Name      *string
				UpdatedAt time.Time
			} {
				var row struct {
					Name      *string
					UpdatedAt time.Time
				}
				require.NoError(t, api.RDB.DB(ctx).Model(tc.record()).Where("token = ?", "record").Take(&row).Error)
				return row
			}
			initial := read().UpdatedAt.Format(time.RFC3339Nano)
			require.NoError(t, tc.update(api, ctx, &initial, "winner"))
			winner := read()
			require.Equal(t, "winner", *winner.Name)
			require.NotEqual(t, initial, winner.UpdatedAt.Format(time.RFC3339Nano))
			require.ErrorIs(t, tc.update(api, ctx, &initial, "stale"), tc.conflict)
			require.Equal(t, winner, read())
			require.NoError(t, tc.update(api, ctx, nil, "legacy"))
			legacy := read()
			require.Equal(t, "legacy", *legacy.Name)
			require.True(t, legacy.UpdatedAt.After(winner.UpdatedAt))
			oldVersion := winner.UpdatedAt.Format(time.RFC3339Nano)
			require.ErrorIs(t, tc.update(api, ctx, &oldVersion, "stale after legacy"), tc.conflict)

			// A competing update lands after the read, before the guarded write.
			expected := legacy.UpdatedAt.Format(time.RFC3339Nano)
			fired := false
			const callback = "test:entity_expected_updated_at_storage_race"
			require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
				if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != tc.table || tx.Error != nil || tx.RowsAffected == 0 {
					return
				}
				fired = true
				tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).Model(tc.record()).
					Where("token = ?", "record").Updates(map[string]any{"name": "race winner", "updated_at": legacy.UpdatedAt.Add(time.Second)}).Error)
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
			require.ErrorIs(t, tc.update(api, ctx, &expected, "race loser"), tc.conflict)
			require.True(t, fired)
			stored := read()
			require.Equal(t, "race winner", *stored.Name)
			require.Equal(t, legacy.UpdatedAt.Add(time.Second), stored.UpdatedAt)
		})
	}
}
