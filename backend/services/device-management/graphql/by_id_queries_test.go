// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"gorm.io/gorm"
)

// The nine `*ById` queries that no other test names: customersById, areaTypesById,
// entityGroupsById, assetTypesById, detectionRulesById, provisioningProfilesById,
// entityRelationshipTypesById, entityRelationshipsById and customerTypesById.
//
// They share one contract and one implementation (rdb.FindByIds behind a resolver that
// checks the authority first), so they share one table: another tenant's id returns nothing
// (the tenant-scope callback, not the resolver, is what holds that line), an unknown id is
// skipped, an empty list answers an empty list rather than the table, a list over the
// lookup bound is refused with the typed LIMIT_EXCEEDED error, and a caller without
// device:read is refused before any row is read.

type byIdCase struct {
	name string
	// seed writes one row of the kind under the tenant in ctx and returns its id and token.
	seed func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint
	// call runs the resolver and returns the ids it answered with.
	call func(ctx context.Context, ids []string) ([]uint, error)
}

func create(t *testing.T, ctx context.Context, db *gorm.DB, row any, id func() uint) uint {
	t.Helper()
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		t.Fatalf("seed %T: %v", row, err)
	}
	return id()
}

func byIdCases() []byIdCase {
	r := &SchemaResolver{}
	return []byIdCase{
		{
			name: "customersById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.Customer{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.CustomersById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "customerTypesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.CustomerType{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.CustomerTypesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "areaTypesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.AreaType{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.AreaTypesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "assetTypesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.AssetType{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.AssetTypesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "entityGroupsById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.EntityGroup{MemberType: "device", MembershipMode: "static"}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.EntityGroupsById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "detectionRulesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.DetectionRule{Definition: []byte(`{}`)}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.DetectionRulesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "provisioningProfilesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.ProvisioningProfile{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.ProvisioningProfilesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "entityRelationshipTypesById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.EntityRelationshipType{}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.EntityRelationshipTypesById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
		{
			name: "entityRelationshipsById",
			seed: func(t *testing.T, ctx context.Context, db *gorm.DB, token string) uint {
				row := &model.EntityRelationship{SourceType: "device", SourceId: 1, TargetType: "device", TargetId: 2}
				row.Token = token
				return create(t, ctx, db, row, func() uint { return row.ID })
			},
			call: func(ctx context.Context, ids []string) ([]uint, error) {
				got, err := r.EntityRelationshipsById(ctx, struct{ Ids []string }{ids})
				out := make([]uint, 0, len(got))
				for _, g := range got {
					out = append(out, g.M.ID)
				}
				return out, err
			},
		},
	}
}

// byIdWorld builds a database holding the nine kinds and returns a context for the "acme"
// tenant with device:read. Each case seeds its own rows, so one case cannot satisfy another.
func byIdWorld(t *testing.T) (context.Context, *gorm.DB) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Customer{}, &model.CustomerType{}, &model.AreaType{}, &model.AssetType{},
		&model.EntityGroup{}, &model.DetectionRule{}, &model.ProvisioningProfile{},
		&model.EntityRelationshipType{}, &model.EntityRelationship{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return withAuthorities(ctx, auth.DeviceRead), db
}

func idStr(id uint) string { return strconv.FormatUint(uint64(id), 10) }

func TestByIdQueriesTenantScopingEmptyInputAndBound(t *testing.T) {
	for _, tc := range byIdCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := byIdWorld(t)
			other := core.WithTenant(ctx, "globex")

			mine1 := tc.seed(t, ctx, db, "mine-1")
			mine2 := tc.seed(t, ctx, db, "mine-2")
			theirs := tc.seed(t, other, db, "theirs-1")

			// The rows the query returns for the caller's own ids.
			got, err := tc.call(ctx, []string{idStr(mine1), idStr(mine2)})
			if err != nil {
				t.Fatalf("own ids: %v", err)
			}
			if len(got) != 2 || !contains(got, mine1) || !contains(got, mine2) {
				t.Fatalf("own ids returned %v, want %d and %d", got, mine1, mine2)
			}

			// Another tenant's id returns nothing, alone or mixed with the caller's own.
			got, err = tc.call(ctx, []string{idStr(theirs)})
			if err != nil || len(got) != 0 {
				t.Fatalf("another tenant's id returned %v (err %v), want nothing", got, err)
			}
			got, err = tc.call(ctx, []string{idStr(mine1), idStr(theirs)})
			if err != nil || len(got) != 1 || got[0] != mine1 {
				t.Fatalf("mixed ids returned %v (err %v), want only %d", got, err, mine1)
			}
			// And the reverse: the other tenant sees its own row and not the caller's.
			got, err = tc.call(withAuthorities(other, auth.DeviceRead), []string{idStr(mine1), idStr(theirs)})
			if err != nil || len(got) != 1 || got[0] != theirs {
				t.Fatalf("the other tenant's mixed ids returned %v (err %v), want only %d", got, err, theirs)
			}

			// An unknown id is skipped, not an error.
			got, err = tc.call(ctx, []string{idStr(mine1), "999999"})
			if err != nil || len(got) != 1 || got[0] != mine1 {
				t.Fatalf("an unknown id returned %v (err %v), want only %d", got, err, mine1)
			}

			// An empty list is an empty list, not the table.
			got, err = tc.call(ctx, []string{})
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("an empty list returned %v (err %v), want an empty list", got, err)
			}

			// An over-bound list is refused with the typed LIMIT_EXCEEDED error, and the
			// bound itself is accepted.
			ids := make([]string, 0, rdb.MaxLookupKeys+1)
			for i := 1; i <= rdb.MaxLookupKeys; i++ {
				ids = append(ids, strconv.Itoa(i+1000))
			}
			if _, err = tc.call(ctx, ids); err != nil {
				t.Fatalf("a list of exactly the bound (%d) was refused: %v", rdb.MaxLookupKeys, err)
			}
			ids = append(ids, "1")
			_, err = tc.call(ctx, ids)
			var lim *limit.Error
			if !errors.As(err, &lim) {
				t.Fatalf("a list of %d ids: err %v, want a limit error", len(ids), err)
			}
			if code := lim.Extensions()["code"]; code != "LIMIT_EXCEEDED" {
				t.Fatalf("over-bound code %v, want LIMIT_EXCEEDED", code)
			}
		})
	}
}

// Every resolver checks device:read before reading: an unauthenticated caller and one holding
// only an unrelated authority are refused, and a caller holding it is admitted.
func TestByIdQueriesRequireDeviceRead(t *testing.T) {
	for _, tc := range byIdCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := byIdWorld(t)
			id := tc.seed(t, ctx, db, "mine-1")
			ids := []string{idStr(id)}

			anonymous := auth.WithClaims(ctx, nil)
			if got, err := tc.call(anonymous, ids); !errors.Is(err, auth.ErrUnauthenticated) || len(got) != 0 {
				t.Fatalf("unauthenticated: %v (err %v), want ErrUnauthenticated and nothing", got, err)
			}
			unrelated := withAuthorities(ctx, auth.NotificationRead)
			if got, err := tc.call(unrelated, ids); !errors.Is(err, auth.ErrForbidden) || len(got) != 0 {
				t.Fatalf("without device:read: %v (err %v), want ErrForbidden and nothing", got, err)
			}
			if got, err := tc.call(ctx, ids); err != nil || len(got) != 1 {
				t.Fatalf("with device:read: %v (err %v), want the row", got, err)
			}
		})
	}
}

func contains(ids []uint, want uint) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
