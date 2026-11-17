// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Run with DC_IT_PGPORT / DC_IT_PGPASSWORD, using the real migration harness.
// Force both lock orders; a start-at-once race alone could miss the unsafe window.
func TestTOCTOUTrackingFlipAndDeviceCreateCannotBothSucceed(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	for _, first := range []string{"SHARE", "UPDATE"} {
		t.Run(first, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("tracking-%s-%d", first, time.Now().UnixNano())), 45*time.Second)
			defer cancel()
			seedTrackingFixture(t, api, ctx)
			locked := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var once sync.Once
			name := "test:tracking-lock"
			if err := api.RDB.Database.Callback().Query().After("gorm:query").Register(name, func(db *gorm.DB) {
				lock, ok := db.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if ok && lock.Strength == first && db.Statement.Table == "entity_relationship_types" {
					once.Do(func() {
						close(locked)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer api.RDB.Database.Callback().Query().Remove(name)
			createDone := make(chan error, 1)
			flipDone := make(chan error, 1)
			create := func() {
				_, err := api.CreateEntityRelationship(ctx, trackingEdge(tenantForTracking(ctx), true, "bravo"))
				createDone <- err
			}
			flip := func() {
				_, err := api.UpdateEntityRelationshipType(ctx, "custom", trackingRequest(true))
				flipDone <- err
			}
			if first == "SHARE" {
				go create()
			} else {
				go flip()
			}
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal("first operation never took its row lock")
			}
			if first == "SHARE" {
				go flip()
			} else {
				go create()
			}
			waitForBlockedBackend(t, api.RDB.Database.WithContext(core.WithSystemContext(ctx)))
			unblock()
			var createErr, flipErr error
			select {
			case createErr = <-createDone:
			case <-ctx.Done():
				t.Fatal("create timed out")
			}
			select {
			case flipErr = <-flipDone:
			case <-ctx.Done():
				t.Fatal("flip timed out")
			}
			if first == "SHARE" {
				var refusal *ErrDeviceAuthoredEdgesBlockTracking
				if createErr != nil || !errors.As(flipErr, &refusal) || refusal.Count != 1 {
					t.Fatalf("create first: create=%v flip=%v", createErr, flipErr)
				}
			} else if flipErr != nil || !errors.Is(createErr, ErrDeviceAuthoredOwnership) {
				t.Fatalf("flip first: create=%v flip=%v", createErr, flipErr)
			}
		})
	}
}

func tenantForTracking(ctx context.Context) string {
	tenant, _ := core.TenantFromContext(ctx)
	return tenant
}
