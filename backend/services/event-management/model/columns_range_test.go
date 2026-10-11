// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"math"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// A classifier is a uint stored in a bigint. One above the bigint range must be refused,
// not wrapped into a negative number and stored: the VALUES write it replaced failed on
// such a value, and a stored wrapped value reads back as a different classifier.
func TestAClassifierAboveTheBigintRangeIsRefused(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	big := uint(math.MaxInt64) + 5
	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{{
		Event:             Event{EventId: []byte("e"), DeviceToken: "d", EventType: esmodel.Measurement, OccurredTime: at},
		EntryOccurredTime: at, Name: "m", Value: f64(1), Classifier: &big}})
	require.Error(t, err, "a classifier of %d was accepted", big)
	var n int64
	require.NoError(t, api.RDB.DB(core.WithSystemContext(context.Background())).
		Model(&MeasurementEvent{}).Count(&n).Error)
	require.Zero(t, n, "a refused measurement was stored")
}
