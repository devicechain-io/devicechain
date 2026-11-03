// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The plain INSERT must really be plain: a stored duplicate is a unique violation (the
// negative control for the writer's fallback, which would otherwise be untested), and the
// same rows under the default context are absorbed. Both parents and payload rows.
func TestPlainInsertRefusesADuplicateAndTheArbiterAbsorbsIt(t *testing.T) {
	api := newPostgresApi(t, "emplaintest")
	tenant := "plain-t"
	base := core.WithTenant(context.Background(), tenant)
	at := time.Now().UTC().Truncate(time.Microsecond)
	build := func() *EventRows {
		ev := Event{DeviceToken: "d1", EventType: eventtype.Measurement, OccurredTime: at, Source: "t", ProcessedTime: at}
		id, err := DeriveEventIdForPayload(tenant, &ev, nil)
		require.NoError(t, err)
		ev.EventId = id
		v := 1.5
		req := &MeasurementEventCreateRequest{Event: ev, EntryOccurredTime: at, Name: "m", Value: &v}
		parents, rows, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{req})
		require.NoError(t, err)
		return &EventRows{Parents: parents, Measurements: rows}
	}
	write := func(ctx context.Context) error {
		return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error { return api.CreateEventRows(ctx, tx, build()) })
	}
	require.NoError(t, write(rdb.WithPlainInsert(base)), "the first plain write stores the rows")
	err := write(rdb.WithPlainInsert(base))
	require.Error(t, err, "a plain write of stored rows must fail")
	require.True(t, rdb.IsUniqueViolation(err), "as a unique violation, got %v", err)
	require.NoError(t, write(base), "the arbiter absorbs the duplicate")
	var n int64
	require.NoError(t, api.RDB.DB(base).Model(&MeasurementEvent{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
}
