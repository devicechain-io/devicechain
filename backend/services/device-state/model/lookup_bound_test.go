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
