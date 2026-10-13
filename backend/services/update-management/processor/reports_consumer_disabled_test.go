// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

// 🔴 A DISABLED DEVICE'S UPDATE TRAFFIC IS SETTLED ON ITS FIRST DELIVERY AND COUNTED. Retrying cannot
// answer differently while the device stays disabled, and a dead letter per message would move a
// flood into the dead-letter store. Each kind the consumer applies is counted under
// update_reports_rejected_total{reason="device_disabled"}, answered with nothing, and acked.
func TestADisabledDevicesUpdateReportsAreCountedAndSettled(t *testing.T) {
	disabled := fmt.Errorf("applying: %w", model.ErrDeviceDisabled)
	api := &fakeApi{applyErr: disabled, grantErr: disabled, currentErr: disabled}
	c, reg := newConsumerWithRegistry(t, api)

	bodies := map[string]string{
		"update.progress":  progressBody("RECEIVED"),
		"update.inventory": `{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b","running":{"version":"1","digest":"sha256:` + fmt64() + `"}}`,
		"download.request": `{"v":1,"kind":"download.request","assignmentId":"asg-1"}`,
		"sync":             `{"v":1,"kind":"sync","bootId":"b","config":{"haveRevision":0}}`,
	}
	n := 0
	for kind, body := range bodies {
		ack := &ackRecorder{}
		c.handle(context.Background(), messaging.NewConsumedMessage(reportsSubject, []byte(body), 1, nil, ack))
		n++
		require.Equal(t, 1, ack.acks, "%s from a disabled device was left for redelivery", kind)
		require.EqualValues(t, n, counter(c, "rejected", rejectedDeviceDisabled), kind)
	}
	require.EqualValues(t, 0, counter(c, "grants", "error"),
		"a disabled device's download request was counted as a grant error")
	require.Empty(t, api.published, "a disabled device was given a grant")
	require.Zero(t, api.answers, "a disabled device was answered a sync")
	require.EqualValues(t, n, handled(t, reg, core.ResultDropped))
	require.Zero(t, handled(t, reg, core.ResultRetry))
	require.Zero(t, handled(t, reg, core.ResultFailed))
}

func fmt64() string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
