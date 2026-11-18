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
	"github.com/stretchr/testify/require"
)

// TestCreateEnrollMarkerHealOnPostgres: on the real engine, with the family advisory lock live, a
// group version registered by an earlier release (no marker) is healed by the cross-tenant pass,
// and a heal racing a create cannot leave the new device out: either the create waits on the lock
// and reads the committed marker, or the rebuild reads the committed device.
func TestCreateEnrollMarkerHealOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	setup := func(t *testing.T, base context.Context, name string) (context.Context, *EntityGroup, int32) {
		t.Helper()
		ctx := core.WithTenant(base, fmt.Sprintf("heal-%s-%d", name, time.Now().UnixNano()))
		_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
		require.NoError(t, err)
		g := createDynamicGroup(t, api, ctx, "fresh", `!("retired" in attr)`)
		v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
		require.NoError(t, err)
		require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
		asEarlierRelease(t, api, ctx, g, v.Version)
		return ctx, g, v.Version
	}

	t.Run("pass", func(t *testing.T) {
		ctx, g, v := setup(t, context.Background(), "pass")
		missed, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "missed", DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		require.False(t, isMemberOf(t, api, ctx, "device", missed.ID, g.ID, v), "premise")
		_, err = NewCreateEnrollMarkerHealer(api).Pass(context.Background())
		require.NoError(t, err)
		require.True(t, hasMarker(t, api, ctx, g, v))
		require.True(t, isMemberOf(t, api, ctx, "device", missed.ID, g.ID, v))
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "new", DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		require.True(t, isMemberOf(t, api, ctx, "device", d.ID, g.ID, v))
	})

	t.Run("race", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				base, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				ctx, g, v := setup(t, base, fmt.Sprintf("race-%d", i))
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
						_, err := NewCreateEnrollMarkerHealer(worker).heal(ctx, enrollMarkerKey{g.ID, v}, false)
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
						t.Fatal("heal/create race did not finish within 10 seconds:", ctx.Err())
					}
				}
				d := <-created
				require.NotNil(t, d)
				require.True(t, hasMarker(t, api, ctx, g, v))
				require.True(t, isMemberOf(t, api, ctx, "device", d.ID, g.ID, v))
			})
		}
	})
}
