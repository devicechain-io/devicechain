// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResolvedGroupStartupRefusesUntilFactRouting(t *testing.T) {
	ms := &core.Microservice{InstanceId: "group-refusal", FunctionalArea: "event-processing"}
	ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}
	nm := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), nil)
	require.ErrorContains(t, createNatsComponents(nm, nil), "S2b fact routing")
}
