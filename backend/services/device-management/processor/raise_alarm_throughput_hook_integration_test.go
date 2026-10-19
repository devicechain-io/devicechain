// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// newThroughputConsumer builds the consumer the way the throughput test needs it.
func newThroughputConsumer(api model.DeviceManagementApi, reader messaging.MessageReader, workers int) *RaiseAlarmConsumer {
	rc := &RaiseAlarmConsumer{Api: api, Reader: reader, Workers: workers}
	rc.procCtx = context.Background()
	return rc
}
