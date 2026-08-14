// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// deviceIdTestCtx builds a real sqlite-backed device-management Api holding three
// devices, in a tenant context. Three, not one, so "returned nothing" and "returned
// the table" are different-looking results.
func deviceIdTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	api := model.NewApi(&rdb.RdbManager{Database: db})
	for _, token := range []string{"pump-01", "pump-02", "pump-03"} {
		device := &model.Device{}
		device.Token = token
		if err := api.RDB.DB(ctx).Create(device).Error; err != nil {
			t.Fatalf("seed %s: %v", token, err)
		}
	}
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// devicesById with an EMPTY id list must answer with nothing.
//
// The whole path, not just the lookup: `devicesById(ids: [])` is a legal document,
// util.AsUintIds turns it into an empty []uint without complaint, and gorm's
// inline-condition form then dropped the filter and returned every device in the
// tenant — an unbounded, unpaginated read reachable by any caller holding the
// device:read that every enabled tenant member already has. Measured on this code
// path before the fix, which is why it is asserted here rather than only over
// rdb.FindByIds: the helper's own test cannot see a resolver that stops calling it.
func TestDevicesByIdWithNoIdsReturnsNoDevices(t *testing.T) {
	ctx := withAuthorities(deviceIdTestCtx(t), auth.DeviceRead)
	r := &SchemaResolver{}

	found, err := r.DevicesById(ctx, struct{ Ids []string }{Ids: []string{}})
	if err != nil {
		t.Fatalf("devicesById([]): %v", err)
	}
	if len(found) != 0 {
		tokens := make([]string, 0, len(found))
		for _, d := range found {
			tokens = append(tokens, d.Token())
		}
		t.Fatalf("devicesById([]) returned %d devices %v, want none — the id filter was dropped",
			len(found), tokens)
	}
}

// The counterweight: a resolver that always returned an empty list would satisfy the
// test above while breaking every device lookup in the console.
func TestDevicesByIdStillReturnsTheDeviceAsked(t *testing.T) {
	ctx := withAuthorities(deviceIdTestCtx(t), auth.DeviceRead)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	r := &SchemaResolver{}

	seeded, err := api.DevicesByToken(ctx, []string{"pump-02"})
	if err != nil || len(seeded) != 1 {
		t.Fatalf("load the seeded device: got %d err=%v", len(seeded), err)
	}

	found, err := r.DevicesById(ctx, struct{ Ids []string }{Ids: []string{fmt.Sprint(seeded[0].ID)}})
	if err != nil {
		t.Fatalf("devicesById([id]): %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("devicesById([id]) returned %d devices, want 1", len(found))
	}
	if got := found[0].Token(); got != "pump-02" {
		t.Errorf("devicesById returned %q, want pump-02", got)
	}
}
