// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRetentionSweepIsolatesTenantFailure(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		states, ledger, allFail bool
	}{
		{"states", true, false, false},
		{"attempts", false, true, false},
		{"combined", true, true, false},
		{"combined all fail", true, true, true},
		{"combined one tenant fails", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := rdb.RegisterTenantScoping(db); err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&model.NotificationState{}, &model.NotificationAttempt{}); err != nil {
				t.Fatal(err)
			}
			manager := &rdb.RdbManager{Database: db}
			tenants := []string{"A", "B"}
			if tc.name == "combined one tenant fails" {
				tenants = []string{"A"}
			}
			for _, tenant := range tenants {
				var rows []any
				if tc.states {
					rows = append(rows, &model.NotificationState{AlarmToken: tenant})
				}
				if tc.ledger {
					rows = append(rows, &model.NotificationAttempt{AlarmToken: tenant, CreatedAt: time.Now().Add(-24 * time.Hour)})
				}
				for _, row := range rows {
					if err := manager.DB(core.WithTenant(context.Background(), tenant)).Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			var visited []string
			if err := db.Callback().Delete().Before("gorm:delete").Register("test:fail-first-tenant", func(tx *gorm.DB) {
				tenant, _ := core.TenantFromContext(tx.Statement.Context)
				visited = append(visited, tenant)
				if tc.allFail || tenant == "A" {
					_ = tx.AddError(errors.New("tenant A prune failed"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			s := &RetentionSweeper{Api: &model.Api{RDB: manager}, retention: time.Hour}
			if tc.ledger {
				s.attemptRetention = time.Hour
			}
			err = s.runOnce(context.Background())
			want := append([]string(nil), tenants...)
			if tc.states && tc.ledger {
				want = append(want, tenants...)
			}
			if err == nil || errors.Is(err, core.ErrPassPartial) == tc.allFail || !reflect.DeepEqual(visited, want) {
				t.Fatalf("visited %v, error %v; want %v, partial %v", visited, err, want, !tc.allFail)
			}
		})
	}
}
