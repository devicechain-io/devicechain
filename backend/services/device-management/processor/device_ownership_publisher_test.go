// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 🔴 THE OWNERSHIP WRITER REPORTS A BROKER REFUSAL. The drain deletes a queued fact only once
// its publish returned nil; a refusal swallowed here is a fact deleted unsent, which is the
// loss the queue exists to prevent. The failure is still counted, as every fact writer's is.
func TestTheOwnershipWriterReportsABrokerRefusal(t *testing.T) {
	broker := &captureFactWriter{refuse: errors.New("nats: connection closed")}
	failures := failureCounter()
	w := NewDeviceOwnershipWriter(broker, failures)
	fact := &model.DeviceOwnershipChangedEvent{DeviceToken: "dozer-01", Anchors: []string{"customer:acme"}, ChangedTime: time.Now().UTC()}

	err := w.PublishDeviceOwnershipChanged(context.Background(), fact)
	require.ErrorIs(t, err, broker.refuse, "a refused publish was reported as sent")
	require.Equal(t, 1.0, testutil.ToFloat64(failures))

	broker.refuse = nil
	require.NoError(t, w.PublishDeviceOwnershipChanged(context.Background(), fact))
	require.Len(t, broker.payloads, 1)
	require.Equal(t, 1.0, testutil.ToFloat64(failures), "a delivered fact was counted as a failure")
}
