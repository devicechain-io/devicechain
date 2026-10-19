// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package entityevent

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalEntityDeletedEventRoundTrips(t *testing.T) {
	when := time.Date(2026, 7, 6, 8, 30, 0, 123456789, time.UTC)
	event := &EntityDeletedEvent{
		EntityType:  entity.TypeCustomer,
		EntityId:    4242,
		EntityToken: "acme-corp",
		DeletedTime: when,
	}
	bytes, err := MarshalEntityDeletedEvent(event)
	require.NoError(t, err)

	got, err := UnmarshalEntityDeletedEvent(bytes)
	require.NoError(t, err)
	assert.Equal(t, entity.TypeCustomer, got.EntityType)
	assert.Equal(t, uint(4242), got.EntityId)
	assert.Equal(t, "acme-corp", got.EntityToken)
	assert.True(t, got.DeletedTime.Equal(when), "deleted time must round-trip")
}
