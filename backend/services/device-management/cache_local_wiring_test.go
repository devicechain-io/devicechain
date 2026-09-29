// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 🔑 EVERY CACHE THE SERVICE BUILDS ANSWERS A REPEATED READ FROM MEMORY. Which caches keep
// the in-process tier is decided cache by cache in model.InitializeCaches, and that decision
// is what takes a key-value round-trip off each per-event lookup. Every other test builds
// its caches another way (over an in-memory store, or one at a time), so without this one a
// cache built WithoutLocalCache in InitializeCaches — DeviceByToken, say — would pass every
// test while quietly putting the round-trip back on the hot path.
//
// It goes through InitializeCaches over a real broker, then reads each cache's own
// in-process hit counter after a write and a read: a hit of exactly 1 means the read never
// left the process. A cache with the tier off exports no kv_cache_local_* series at all,
// which fails here as "no series" rather than as a 0.
//
// A cache that must NOT keep the tier is a deliberate change to this list, made together
// with the comment on InitializeCaches that says why.
func TestEveryDeviceManagementCacheKeepsItsInProcessTier(t *testing.T) {
	host, port := startEmbeddedNats(t)

	ms := &core.Microservice{
		InstanceId:     fmt.Sprintf("l1wiring%d", time.Now().UnixNano()),
		FunctionalArea: "device-management",
	}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

	cfg := config.NewDeviceManagementConfiguration()
	caches, err := model.InitializeCaches(nmgr, cfg)
	require.NoError(t, err)

	byName := map[string]*messaging.Cache{
		model.CACHE_NAME_DEVICE_BY_TOKEN:            caches.DeviceByToken,
		model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    caches.RelationshipsBySource,
		model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: caches.ProfileResolutionByType,
		model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      caches.MembershipsByEntity,
		model.CACHE_NAME_SCOPED_GROUPS_EXIST:        caches.ScopedGroupsExist,
	}
	ctx := context.Background()
	for name, c := range byName {
		require.NotNil(t, c, "InitializeCaches built no %s cache", name)
		require.NoError(t, c.Set(ctx, "acme|k", "v"), "set %s", name)
		var got string
		found, err := c.Get(ctx, "acme|k", &got)
		require.NoError(t, err, "get %s", name)
		require.True(t, found && got == "v", "%s read back (%v, %q), want (true, \"v\")", name, found, got)
	}

	hits := map[string]float64{}
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "devicechain_devicemanagement_kv_cache_local_lookups_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			var cache, result string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "cache":
					cache = l.GetValue()
				case "result":
					result = l.GetValue()
				}
			}
			if result == "hit" {
				hits[cache] = m.GetCounter().GetValue()
			}
		}
	}
	for name := range byName {
		got, ok := hits[name]
		if !ok {
			t.Errorf("%s exports no in-process hit series: it was built with the in-process tier off", name)
			continue
		}
		if got != 1 {
			t.Errorf("%s in-process hits after a write and a read = %v, want 1", name, got)
		}
	}
}
