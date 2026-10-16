// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// metricVocabularyCtx is a tenant-"acme" context over a real sqlite Api holding one
// device in acme and one in globex, carrying no claims.
func metricVocabularyCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.GeoFenceSetVersion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := keyedApi(db)
	for _, seed := range []struct{ tenant, token string }{{"acme", "acme-dev"}, {"globex", "globex-dev"}} {
		d := &model.Device{}
		d.Token = seed.token
		if err := api.RDB.DB(core.WithTenant(context.Background(), seed.tenant)).Create(d).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.token, err)
		}
	}
	ctx := core.WithTenant(context.Background(), "acme")
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

func callMetricVocabulary(ctx context.Context, token string) (*DeviceMetricVocabularyResolver, error) {
	return (&SchemaResolver{}).DeviceMetricVocabulary(ctx, struct{ DeviceToken string }{DeviceToken: token})
}

// The query is gated: a caller with no claims is refused, not answered.
func TestDeviceMetricVocabularyRefusesUnauthenticated(t *testing.T) {
	v, err := callMetricVocabulary(metricVocabularyCtx(t), "acme-dev")
	if err == nil || v != nil {
		t.Fatalf("a caller with no claims got %v, %v", v, err)
	}
}

// device:read is enough for the caller's own tenant's device, and a device of another
// tenant answers exactly as a token that does not exist: null, no error.
func TestDeviceMetricVocabularyIsTenantScoped(t *testing.T) {
	ctx := withAuthorities(metricVocabularyCtx(t), auth.DeviceRead)
	own, err := callMetricVocabulary(ctx, "acme-dev")
	if err != nil {
		t.Fatalf("own device: %v", err)
	}
	if own == nil {
		t.Fatal("device:read on the caller's own device answered null")
	}
	other, err := callMetricVocabulary(ctx, "globex-dev")
	if err != nil {
		t.Fatalf("other tenant's device: %v", err)
	}
	if other != nil {
		t.Fatal("another tenant's device resolved for a caller in acme")
	}
}
