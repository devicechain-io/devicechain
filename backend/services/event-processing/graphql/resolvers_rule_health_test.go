// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// healthStores is the three stores ruleHealth joins, over one sqlite database, plus its handle.
func healthStores(t *testing.T) (*model.DetectRuleStore, *model.ProfileActiveStore, *model.RuleStatStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.DetectRule{}, &model.ProfileActive{}, &model.RuleStat{}, &model.RuleStatSweepRecord{}); err != nil {
		t.Fatal(err)
	}
	m := &rdb.RdbManager{Database: db}
	return model.NewDetectRuleStore(m), model.NewProfileActiveStore(m), model.NewRuleStatStore(m), db
}

// 🔴 A rollback can make a version active again after the sweep removed its history. ruleHealth
// must then say the history was removed, not report a rule that fired as one that never did;
// a version that was never swept reports no removal.
func TestRuleHealthReportsAVersionWhoseHistoryWasRemoved(t *testing.T) {
	rs, ps, stats, db := healthStores(t)
	evPublish(t, rs, ps, "acme", "boiler", "1",
		model.DetectRule{RuleToken: "hot", Definition: hotDefinition},
		model.DetectRule{RuleToken: "cold", Definition: hotDefinition})
	r := &SchemaResolver{DetectRules: rs, Profiles: ps, RuleStats: stats}
	ctx := previewCtx(auth.DeviceRead)

	// Never swept: no removal, and an unfired rule reads as never fired.
	got, err := r.RuleHealth(ctx, struct{ ProfileToken string }{"boiler"})
	if err != nil || len(got) != 2 {
		t.Fatalf("RuleHealth = %d rows, %v", len(got), err)
	}
	for _, h := range got {
		if h.HistoryRemovedAt() != nil {
			t.Fatalf("%s: historyRemovedAt %q on a version never swept", h.RuleToken(), *h.HistoryRemovedAt())
		}
	}

	// The sweep removed boiler@1's history (as SweepStranded records it), and since the rollback
	// one rule fired once.
	swept := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	tctx := dccore.WithTenant(context.Background(), "acme")
	if err := db.WithContext(tctx).Create(&model.RuleStatSweepRecord{Tenant: "acme", ProfileVersionToken: "boiler@1", SweptAt: swept}).Error; err != nil {
		t.Fatal(err)
	}
	// Another tenant's record for the same version token must not leak in.
	if err := db.WithContext(dccore.WithTenant(context.Background(), "other")).Create(&model.RuleStatSweepRecord{Tenant: "other", ProfileVersionToken: "boiler@2", SweptAt: swept}).Error; err != nil {
		t.Fatal(err)
	}
	if err := stats.RecordFire(tctx, "acme/boiler@1/hot", "acme", swept.Add(time.Hour), "raised"); err != nil {
		t.Fatal(err)
	}
	got, err = r.RuleHealth(ctx, struct{ ProfileToken string }{"boiler"})
	if err != nil || len(got) != 2 {
		t.Fatalf("RuleHealth = %d rows, %v", len(got), err)
	}
	for _, h := range got {
		at := h.HistoryRemovedAt()
		if at == nil || *at != swept.Format(time.RFC3339Nano) {
			t.Fatalf("%s: historyRemovedAt = %v; want %s", h.RuleToken(), at, swept)
		}
		switch h.RuleToken() {
		case "hot":
			if h.FireCount() != 1 || h.LastFiredAt() == nil {
				t.Errorf("hot: %d fires, last %v; want the one fire since the removal", h.FireCount(), h.LastFiredAt())
			}
		case "cold":
			if h.FireCount() != 0 || h.LastFiredAt() != nil {
				t.Errorf("cold: %d fires, last %v; want none since the removal", h.FireCount(), h.LastFiredAt())
			}
		}
	}

	// A version made active by a later publish has its own (absent) record.
	evPublish(t, rs, ps, "acme", "boiler", "2", model.DetectRule{RuleToken: "hot", Definition: hotDefinition})
	got, err = r.RuleHealth(ctx, struct{ ProfileToken string }{"boiler"})
	if err != nil || len(got) != 1 || got[0].HistoryRemovedAt() != nil {
		t.Fatalf("boiler@2: %d rows, %v; want one row with no removal (and no other tenant's)", len(got), err)
	}
}
