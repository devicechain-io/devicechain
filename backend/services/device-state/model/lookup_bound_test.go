// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func keyList(n int) []string {
	k := make([]string, n)
	for i := range k {
		k[i] = fmt.Sprintf("k-%d", i)
	}
	return k
}

// Every batch lookup refuses a key list over rdb.MaxLookupKeys with the typed limit
// refusal, and answers one at the bound.
func TestBatchLookupsAreBounded(t *testing.T) {
	api := newTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&LatestLocation{}); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "A")

	lookups := map[string]func([]string) error{
		"DeviceStatesByDeviceToken": func(k []string) error { _, err := api.DeviceStatesByDeviceToken(ctx, k); return err },
		"DeviceStatesByExternalId":  func(k []string) error { _, err := api.DeviceStatesByExternalId(ctx, k); return err },
		"LatestLocationsByDeviceToken": func(k []string) error {
			_, err := api.LatestLocationsByDeviceToken(ctx, k)
			return err
		},
	}
	for name, call := range lookups {
		t.Run(name, func(t *testing.T) {
			if err := call(keyList(rdb.MaxLookupKeys)); err != nil {
				t.Fatalf("a list at the bound must be answered: %v", err)
			}
			if _, ok := limit.As(call(keyList(rdb.MaxLookupKeys + 1))); !ok {
				t.Fatal("a list over the bound must be refused with the limit refusal")
			}
		})
	}
}

// Below the bound the lookups return the right rows: the external-id lookup matches on the
// external id (not the device token), the device-token lookup does not match external ids,
// another tenant's row never appears, and locations come back device-token ordered.
func TestBatchLookupsBelowTheBoundReturnTheRightRowsInOrder(t *testing.T) {
	api := newTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&LatestLocation{}); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "A")
	other := core.WithTenant(context.Background(), "B")
	db := api.RDB.Database
	for _, row := range []*DeviceState{
		{DeviceToken: "dev-1", ExternalId: "ext-2"},
		{DeviceToken: "dev-2", ExternalId: "ext-1"},
		{DeviceToken: "dev-3", ExternalId: "ext-3"},
	} {
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(other).Create(&DeviceState{DeviceToken: "dev-1", ExternalId: "ext-1"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"loc-c", "loc-a", "loc-b"} {
		if err := db.WithContext(ctx).Create(&LatestLocation{DeviceToken: tok}).Error; err != nil {
			t.Fatal(err)
		}
	}

	byExt, err := api.DeviceStatesByExternalId(ctx, []string{"ext-1", "ext-3", "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range byExt {
		got = append(got, r.DeviceToken)
	}
	if len(got) != 2 || !((got[0] == "dev-2" && got[1] == "dev-3") || (got[0] == "dev-3" && got[1] == "dev-2")) {
		t.Fatalf("external-id lookup returned %v, want dev-2 and dev-3 (ext-1, ext-3; not the token \"dev-1\", not tenant B)", got)
	}

	byTok, err := api.DeviceStatesByDeviceToken(ctx, []string{"dev-1", "ext-1"})
	if err != nil || len(byTok) != 1 || byTok[0].ExternalId != "ext-2" {
		t.Fatalf("device-token lookup must match on the token only and stay in tenant A: %v %v", byTok, err)
	}

	locs, err := api.LatestLocationsByDeviceToken(ctx, []string{"loc-c", "loc-a", "loc-b", "loc-none"})
	if err != nil || len(locs) != 3 {
		t.Fatalf("locations: %v %v", locs, err)
	}
	for i, want := range []string{"loc-a", "loc-b", "loc-c"} {
		if locs[i].DeviceToken != want {
			t.Fatalf("locations must be device-token ordered: position %d is %s, want %s", i, locs[i].DeviceToken, want)
		}
	}
}

// The demotion walk's token narrowing is bounded like every other key list.
func TestAssertedStatesForDemotionRefusesMoreThanTheBound(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	over := keyList(rdb.MaxLookupKeys + 1)
	if _, ok := limit.As(func() error {
		_, err := api.AssertedStatesForDemotion(ctx, "mqtt1", &over, 0, 10)
		return err
	}()); !ok {
		t.Fatal("a token list over the bound must be refused")
	}
	atBound := keyList(rdb.MaxLookupKeys)
	if _, err := api.AssertedStatesForDemotion(ctx, "mqtt1", &atBound, 0, 10); err != nil {
		t.Fatalf("a list at the bound must be answered: %v", err)
	}
}
