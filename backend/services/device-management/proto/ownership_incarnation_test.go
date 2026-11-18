// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/require"
)

func TestOwnershipIncarnationRoundTrip(t *testing.T) {
	fact := &model.DeviceOwnershipChangedEvent{DeviceToken: "d", DeviceIncarnation: 23,
		Anchors: []string{"customer:acme"}, ChangedTime: time.Now().UTC()}
	b, err := MarshalDeviceOwnershipChangedEvent(fact)
	require.NoError(t, err)
	got, err := UnmarshalDeviceOwnershipChangedEvent(b)
	require.NoError(t, err)
	require.Equal(t, fact, got)
}
