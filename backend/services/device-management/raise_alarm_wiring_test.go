// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The raise-alarm consumer this service builds resolves devices through the plain Api, not
// the cached one. Through the cached Api, a device deleted on another replica stays
// resolvable from that replica's process memory for a few seconds, and the consumer's drop
// of an edge for a deleted device would not hold (model's
// TestAnotherReplicaResolvesADeletedDeviceUntilItsCopyExpires shows the cached read).
//
// A processor test builds the consumer itself, so only this can see which Api the service
// hands it.
func TestTheRaiseAlarmConsumerResolvesDevicesUncached(t *testing.T) {
	savedMs, savedApi, savedCached, savedMetrics := Microservice, Api, CachedApi, RaiseAlarmMetrics
	t.Cleanup(func() { Microservice, Api, CachedApi, RaiseAlarmMetrics = savedMs, savedApi, savedCached, savedMetrics })

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Api = model.NewApi(&rdb.RdbManager{})
	CachedApi = model.NewCachedApi(Api, &model.Caches{DeviceByToken: msgtest.NewMemoryKV().NewCache()})

	rc := newRaiseAlarmConsumer(nil, nil)
	got, ok := rc.Api.(*model.Api)
	require.True(t, ok, "the raise-alarm consumer was given a %T, want the plain *model.Api", rc.Api)
	require.Same(t, Api, got, "the raise-alarm consumer was given an Api other than this service's")
}
