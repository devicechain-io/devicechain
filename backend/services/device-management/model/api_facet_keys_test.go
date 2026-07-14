// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func newFacetKeyTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&FacetKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// SetFacetKey admits a valid attribute facet, defaults its source to "attribute",
// and fails closed on an unknown member family, an empty key, and a bad value type.
func TestSetFacetKey_Validation(t *testing.T) {
	api := newFacetKeyTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	fk, err := api.SetFacetKey(ctx, &FacetKeySetRequest{
		MemberType: "area", Key: "climate", ValueType: "STRING",
		Values: &[]string{"arid", "temperate"}, Label: strp("Climate"),
	})
	if err != nil {
		t.Fatalf("valid facet rejected: %v", err)
	}
	assert.Equal(t, string(FacetSourceAttribute), fk.Source, "source defaults to attribute")
	assert.NotNil(t, fk.Values)

	cases := []struct {
		name string
		req  *FacetKeySetRequest
	}{
		{"unknown member family", &FacetKeySetRequest{MemberType: "widget", Key: "k", ValueType: "STRING"}},
		{"group member family", &FacetKeySetRequest{MemberType: "group", Key: "k", ValueType: "STRING"}},
		{"empty key", &FacetKeySetRequest{MemberType: "area", Key: "", ValueType: "STRING"}},
		{"over-long key", &FacetKeySetRequest{MemberType: "area", Key: strings.Repeat("k", 129), ValueType: "STRING"}},
		{"bad value type", &FacetKeySetRequest{MemberType: "area", Key: "k", ValueType: "TEXT"}},
	}
	for _, tc := range cases {
		if _, err := api.SetFacetKey(ctx, tc.req); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

// SetFacetKey upserts on the natural key (memberType, key): a second set of the
// same pair rewrites the row rather than creating a duplicate.
func TestSetFacetKey_Upsert(t *testing.T) {
	api := newFacetKeyTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	first, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "area", Key: "climate", ValueType: "STRING", Label: strp("Climate")})
	if err != nil {
		t.Fatalf("first set: %v", err)
	}
	second, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "area", Key: "climate", ValueType: "STRING", Label: strp("Climate zone")})
	if err != nil {
		t.Fatalf("second set: %v", err)
	}
	assert.Equal(t, first.ID, second.ID, "upsert must reuse the same row")

	res, err := api.FacetKeys(ctx, FacetKeySearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	assert.Len(t, res.Results, 1, "upsert must not create a duplicate")
	assert.Equal(t, "Climate zone", res.Results[0].Label.String)
}

// FacetKeys filters by member family.
func TestFacetKeys_Filter(t *testing.T) {
	api := newFacetKeyTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seed := []struct{ mt, key string }{
		{"area", "climate"}, {"area", "country"}, {"device", "firmware_channel"},
	}
	for _, s := range seed {
		if _, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: s.mt, Key: s.key, ValueType: "STRING"}); err != nil {
			t.Fatalf("seed %s/%s: %v", s.mt, s.key, err)
		}
	}
	area := "area"
	res, err := api.FacetKeys(ctx, FacetKeySearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		MemberType: &area,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	assert.Len(t, res.Results, 2, "expected two area facets")
	for _, fk := range res.Results {
		assert.Equal(t, "area", fk.MemberType)
	}
}

// DeleteFacetKey removes an attribute facet by natural key, reports absent keys as
// not-removed, and refuses to delete a system facet.
func TestDeleteFacetKey(t *testing.T) {
	api := newFacetKeyTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "area", Key: "climate", ValueType: "STRING"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ok, err := api.DeleteFacetKey(ctx, "area", "climate")
	if err != nil || !ok {
		t.Fatalf("delete existing: ok=%v err=%v", ok, err)
	}
	// A second delete (now absent) reports not-removed, no error.
	ok, err = api.DeleteFacetKey(ctx, "area", "climate")
	assert.NoError(t, err)
	assert.False(t, ok, "deleting an absent facet reports not-removed")

	// The natural key frees for reuse after delete (partial-unique on live rows).
	if _, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "area", Key: "climate", ValueType: "LONG"}); err != nil {
		t.Fatalf("re-declare after delete: %v", err)
	}

	// A system facet is non-deletable (SD-5). System facets are never user-created,
	// so seed one directly to exercise the guard.
	sys := &FacetKey{MemberType: "device", Key: "manufacturer", ValueType: "STRING", Source: string(FacetSourceSystem)}
	if err := api.RDB.DB(ctx).Create(sys).Error; err != nil {
		t.Fatalf("seed system facet: %v", err)
	}
	if _, err := api.DeleteFacetKey(ctx, "device", "manufacturer"); err == nil {
		t.Error("expected a system facet to be non-deletable")
	}
	// A set that would land on a system facet is likewise refused.
	if _, err := api.SetFacetKey(ctx, &FacetKeySetRequest{MemberType: "device", Key: "manufacturer", ValueType: "STRING"}); err == nil {
		t.Error("expected a set on a system facet to be refused")
	}
}
