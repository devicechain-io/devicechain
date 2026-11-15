// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
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

// Exercise the combined population, including a successful loop after a failed
// loop: classifying either loop early loses the all-failed/partial distinction.
func TestRetentionSweepCombinedOutcomes(t *testing.T) {
	for states := 0; states <= 2; states++ {
		for stateFailures := 0; stateFailures <= states; stateFailures++ {
			for attempts := 0; attempts <= 2; attempts++ {
				for attemptFailures := 0; attemptFailures <= attempts; attemptFailures++ {
					t.Run(fmt.Sprintf("states=%d/%d,attempts=%d/%d", stateFailures, states, attemptFailures, attempts), func(t *testing.T) {
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
						for i := 0; i < states; i++ {
							if err := manager.DB(core.WithTenant(context.Background(), fmt.Sprint(i))).Create(&model.NotificationState{AlarmToken: fmt.Sprint(i)}).Error; err != nil {
								t.Fatal(err)
							}
						}
						for i := 0; i < attempts; i++ {
							if err := manager.DB(core.WithTenant(context.Background(), fmt.Sprint(i))).Create(&model.NotificationAttempt{AlarmToken: fmt.Sprint(i)}).Error; err != nil {
								t.Fatal(err)
							}
						}
						stateErr, attemptErr := errors.New("state failure"), errors.New("attempt failure")
						visits := 0
						if err := db.Callback().Delete().Before("gorm:delete").Register("test:outcomes", func(tx *gorm.DB) {
							visits++
							tenant, _ := core.TenantFromContext(tx.Statement.Context)
							for i := 0; i < stateFailures; i++ {
								if tx.Statement.Schema.Name == "NotificationState" && tenant == fmt.Sprint(i) {
									_ = tx.AddError(stateErr)
								}
							}
							for i := 0; i < attemptFailures; i++ {
								if tx.Statement.Schema.Name == "NotificationAttempt" && tenant == fmt.Sprint(i) {
									_ = tx.AddError(attemptErr)
								}
							}
						}); err != nil {
							t.Fatal(err)
						}
						s := &RetentionSweeper{Api: &model.Api{RDB: manager}, retention: time.Hour, attemptRetention: time.Hour}
						err = s.runOnce(context.Background())
						failed, visited := stateFailures+attemptFailures, states+attempts
						if visits != visited {
							t.Fatalf("visits=%d, want %d", visits, visited)
						}
						if failed == 0 {
							if err != nil {
								t.Fatal(err)
							}
						} else if failed == visited && visited > 1 {
							last := stateErr
							if attemptFailures > 0 {
								last = attemptErr
							}
							if !errors.Is(err, last) || errors.Is(err, core.ErrPassPartial) {
								t.Fatalf("want plain failure wrapping %v, got %v", last, err)
							}
						} else if !errors.Is(err, core.ErrPassPartial) {
							t.Fatalf("want partial, got %v", err)
						}
					})
				}
			}
		}
	}
}

func TestRetentionSweepCancellationOutcomes(t *testing.T) {
	for _, ledger := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			for _, cancelAfter := range []int{1, 2} {
				t.Run(fmt.Sprintf("ledger=%v,fail=%v,cancelAfter=%d", ledger, fail, cancelAfter), func(t *testing.T) {
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
					for _, tenant := range []string{"A", "B"} {
						var row any = &model.NotificationState{AlarmToken: tenant}
						if ledger {
							row = &model.NotificationAttempt{AlarmToken: tenant}
						}
						if err := manager.DB(core.WithTenant(context.Background(), tenant)).Create(row).Error; err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					broken := errors.New("prune failure")
					visits := 0
					if err := db.Callback().Delete().Before("gorm:delete").Register("test:failure", func(tx *gorm.DB) {
						if fail {
							_ = tx.AddError(broken)
						}
					}); err != nil {
						t.Fatal(err)
					}
					if err := db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register("test:cancel", func(tx *gorm.DB) {
						visits++
						if visits == cancelAfter {
							cancel()
						}
					}); err != nil {
						t.Fatal(err)
					}
					s := &RetentionSweeper{Api: &model.Api{RDB: manager}, retention: time.Hour}
					if ledger {
						s.attemptRetention = time.Hour
					}
					err = s.runOnce(ctx)
					if visits != cancelAfter {
						t.Fatalf("visits=%d, want %d", visits, cancelAfter)
					}
					switch {
					case !ledger && cancelAfter == 1:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("want cancellation, got %v", err)
						}
					case !fail:
						if err != nil {
							t.Fatalf("want nil, got %v", err)
						}
					case cancelAfter == 1:
						if !errors.Is(err, core.ErrPassPartial) {
							t.Fatalf("want partial, got %v", err)
						}
					default:
						if !errors.Is(err, broken) || errors.Is(err, core.ErrPassPartial) {
							t.Fatalf("want plain prune failure, got %v", err)
						}
					}
				})
			}
		}
	}
}
