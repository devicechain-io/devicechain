// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

func TestTenantsWith(t *testing.T) {
	db := newTestDB(t)
	manager := &RdbManager{Database: db}
	for _, row := range []struct{ tenant, name string }{{"B", "keep"}, {"A", "drop"}, {"A", "keep"}, {"B", "keep"}} {
		if err := manager.DB(core.WithTenant(context.Background(), row.tenant)).Create(&widget{Name: row.name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := core.WithTenant(context.Background(), "A")
	got, err := manager.TenantsWith(ctx, &widget{})
	if err != nil || !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("tenants = %v, %v", got, err)
	}
	got, err = manager.TenantsWith(ctx, &widget{}, func(db *gorm.DB) *gorm.DB { return db.Where("name = ?", "drop") })
	if err != nil || !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("filtered tenants = %v, %v", got, err)
	}
}
