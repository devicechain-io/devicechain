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

// 🔑 THE CACHES KEYED BY DEVICE HOLD MORE THAN 4,096 ENTRIES; THE OTHERS DO NOT. With every
// cache at messaging's default bound, a replica that saw more than 4,096 devices within 5 s
// evicted a relationships entry for every event and answered none from memory. On a
// three-node GKE cluster, 48,000 devices drove the relationships cache to one capacity
// eviction per event.
//
// It goes through InitializeCaches, with the default configuration, over a real broker,
// stores 4,097 distinct keys in each cache, and reads each cache's own capacity-eviction
// counter. The per-type and per-tenant caches DO evict once: that proves the counter is
// read from the right registry, and pins their small bound.
//
// It uses nothing the tree before the per-device bound lacked, so there it compiles and
// fails by value: one capacity eviction on each per-device cache.
func TestPerDeviceCachesHoldMoreThanTheDefaultBound(t *testing.T) {
	caches, reg := initializeCachesOverABroker(t, config.NewDeviceManagementConfiguration())

	byName := map[string]*messaging.Cache{
		model.CACHE_NAME_DEVICE_BY_TOKEN:            caches.DeviceByToken,
		model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    caches.RelationshipsBySource,
		model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: caches.ProfileResolutionByType,
		model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      caches.MembershipsByEntity,
		model.CACHE_NAME_SCOPED_GROUPS_EXIST:        caches.ScopedGroupsExist,
	}
	ctx := context.Background()
	for name, c := range byName {
		for i := 0; i < 4097; i++ {
			require.NoError(t, c.Set(ctx, fmt.Sprintf("acme|%d", i), "v"), "set %s #%d", name, i)
		}
	}

	evictions := localSeries(t, reg, "kv_cache_local_evictions_total", "reason", "capacity")
	want := map[string]float64{
		model.CACHE_NAME_DEVICE_BY_TOKEN:            0,
		model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    0,
		model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      0,
		model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: 1,
		model.CACHE_NAME_SCOPED_GROUPS_EXIST:        1,
	}
	for name, w := range want {
		got, ok := evictions[name]
		if !ok {
			t.Errorf("%s exports no capacity-eviction series", name)
			continue
		}
		if got != w {
			t.Errorf("%s capacity evictions after 4,097 distinct keys = %v, want %v", name, got, w)
		}
	}
}

// The configured bounds reach the three per-device caches, and only them, as the bound
// gauges report. Entries and MiB are set to values unlike each other and unlike the
// defaults, so a swap or a dropped setting shows.
func TestConfiguredPerDeviceCacheBoundsReachTheCaches(t *testing.T) {
	cfg := config.NewDeviceManagementConfiguration()
	cfg.InMemoryCache.PerDeviceCacheEntries = 5000
	cfg.InMemoryCache.PerDeviceCacheMiB = 7
	_, reg := initializeCachesOverABroker(t, cfg)

	maxEntries := localSeries(t, reg, "kv_cache_local_max_entries", "", "")
	maxBytes := localSeries(t, reg, "kv_cache_local_max_bytes", "", "")
	for name, want := range map[string][2]float64{
		model.CACHE_NAME_DEVICE_BY_TOKEN:            {5000, 7 << 20},
		model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    {5000, 7 << 20},
		model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      {5000, 7 << 20},
		model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: {4096, 4 << 20},
		model.CACHE_NAME_SCOPED_GROUPS_EXIST:        {4096, 4 << 20},
	} {
		if got, ok := maxEntries[name]; !ok || got != want[0] {
			t.Errorf("%s kv_cache_local_max_entries = %v (present %v), want %v", name, got, ok, want[0])
		}
		if got, ok := maxBytes[name]; !ok || got != want[1] {
			t.Errorf("%s kv_cache_local_max_bytes = %v (present %v), want %v", name, got, ok, want[1])
		}
	}
}

// initializeCachesOverABroker builds the service's caches through InitializeCaches over an
// embedded broker, with their metrics in a registry of their own.
func initializeCachesOverABroker(t *testing.T, cfg *config.DeviceManagementConfiguration) (*model.Caches, *prometheus.Registry) {
	t.Helper()
	host, port := startEmbeddedNats(t)
	ms := &core.Microservice{
		InstanceId:     fmt.Sprintf("l1bounds%d", time.Now().UnixNano()),
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

	caches, err := model.InitializeCaches(nmgr, cfg)
	require.NoError(t, err)
	return caches, reg
}

// localSeries reads one device-management series by cache, keeping only the series whose
// label named by label has value value (every series when label is "").
func localSeries(t *testing.T, reg *prometheus.Registry, name, label, value string) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "devicechain_devicemanagement_"+name {
			continue
		}
		for _, m := range f.GetMetric() {
			var cache string
			keep := label == ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "cache" {
					cache = l.GetValue()
				}
				if label != "" && l.GetName() == label && l.GetValue() == value {
					keep = true
				}
			}
			if !keep {
				continue
			}
			if c := m.GetCounter(); c != nil {
				out[cache] = c.GetValue()
			} else if g := m.GetGauge(); g != nil {
				out[cache] = g.GetValue()
			}
		}
	}
	return out
}
