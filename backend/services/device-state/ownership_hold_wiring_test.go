// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The ownership consumer this service builds carries a store hold. A processor test builds its
// own consumer, so a main.go that stopped passing one would leave every processor test green
// while a database outage dropped ownership facts again, leaving a sold device in its old
// owner's scoped view.
func TestTheOwnershipConsumerIsBuiltWithAStoreHold(t *testing.T) {
	savedMs := Microservice
	t.Cleanup(func() { Microservice = savedMs })
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	require.NotNil(t, newOwnershipProcessor(nil).Hold)
}
