// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
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
}

func TestTenantsWithPlainTenant(t *testing.T) {
	db := newTestDB(t)
	type plainTenant struct{ Tenant string }
	if err := db.AutoMigrate(&plainTenant{}); err != nil {
		t.Fatal(err)
	}
	manager := &RdbManager{Database: db}
	for _, tenant := range []string{"B", "A", "A"} {
		if err := manager.DB(core.WithTenant(context.Background(), tenant)).Create(&plainTenant{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := manager.TenantsWith(core.WithTenant(context.Background(), "A"), &plainTenant{})
	if err != nil || !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("tenants = %v, %v", got, err)
	}
}

func TestTenantsWithRefusesModelWithoutTenant(t *testing.T) {
	manager := &RdbManager{Database: newTestDB(t)}
	got, err := manager.TenantsWith(context.Background(), &gadget{})
	if err == nil || !strings.Contains(err.Error(), "no tenant field") || len(got) != 0 {
		t.Fatalf("tenants = %v, %v; want missing tenant field error", got, err)
	}
}
