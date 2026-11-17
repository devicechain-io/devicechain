// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Hold distinct physical connections before releasing the start barrier, so each
// race exercises two backends even when the scheduler runs one goroutine first.
func onCreateRaceConnection(ctx context.Context, api *Api, ready chan<- struct{}, start <-chan struct{}, work func(*Api) error) error {
	return api.RDB.DB(ctx).Connection(func(conn *gorm.DB) error {
		worker := NewApi(&rdb.RdbManager{Database: conn})
		ready <- struct{}{}
		select {
		case <-start:
			return work(worker)
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

// Uses newPostgresRdbManager's real migration harness and DC_IT_PGHOST,
// DC_IT_PGPORT, DC_IT_PGUSER, DC_IT_PGPASSWORD (defaults localhost:5432,
// postgres/postgres). Run against a throwaway server with -tags integration.
func TestGroupEnrollCreateRaces(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	// Separate pool connections must be available to the two transactions.
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	for _, scenario := range []string{"register", "attribute"} {
		t.Run(scenario, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					base, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					// Isolated tenant per run; repeated invocations never collide with prior data.
					ctx := core.WithTenant(base, fmt.Sprintf("enroll-%s-%d-%d", scenario, time.Now().UnixNano(), i))
					_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
					require.NoError(t, err)
					g := createDynamicGroup(t, api, ctx, "fresh", `!("retired" in attr)`)
					v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
					require.NoError(t, err)
					if scenario == "attribute" {
						require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
						_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor"})
						require.NoError(t, err)
					}
					start := make(chan struct{})
					ready := make(chan struct{}, 2)
					created := make(chan *Device, 1)
					results := make(chan error, 2)
					go func() {
						results <- onCreateRaceConnection(ctx, api, ready, start, func(worker *Api) error {
							d, err := worker.CreateDevice(ctx, &DeviceCreateRequest{Token: "new", DeviceTypeToken: "sensor"})
							created <- d
							return err
						})
					}()
					go func() {
						results <- onCreateRaceConnection(ctx, api, ready, start, func(worker *Api) error {
							if scenario == "register" {
								return worker.RegisterGroupVersionForScoping(ctx, g.Token, v.Version)
							}
							value := "yes"
							_, err := worker.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "other", Scope: string(AttributeScopeShared), AttrKey: "retired", ValueType: string(AttributeValueString), Value: &value})
							return err
						})
					}()
					for n := 0; n < 2; n++ {
						select {
						case <-ready:
						case err := <-results:
							t.Fatalf("reserve race connection: %v", err)
						case <-ctx.Done():
							t.Fatal("could not reserve two race connections:", ctx.Err())
						}
					}
					close(start)
					for n := 0; n < 2; n++ {
						select {
						case err := <-results:
							require.NoError(t, err)
						case <-ctx.Done():
							t.Fatal("membership create race did not finish within 10 seconds:", ctx.Err())
						}
					}
					d := <-created
					require.NotNil(t, d)
					require.True(t, isMemberOf(t, api, ctx, "device", d.ID, g.ID, v.Version))
				})
			}
		})
	}
}
