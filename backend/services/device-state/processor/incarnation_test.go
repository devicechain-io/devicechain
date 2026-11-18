// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

func TestSingleMessageIncarnationAndChildrenMergeAtomically(t *testing.T) {
	sp, db, _ := newFencedStateProcessor(t)
	pool := countTransactions(db)
	ctx := core.WithTenant(context.Background(), fenceTenant)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	message := func(incarnation uint64, occurred time.Time) messaging.Message {
		msg := threeMetrics(t, "d", occurred)
		e, err := resolvedevent.UnmarshalResolvedEvent(msg.Value)
		require.NoError(t, err)
		e.DeviceIncarnation = incarnation
		msg.Value, err = resolvedevent.MarshalResolvedEvent(e)
		require.NoError(t, err)
		return msg
	}
	seed(t, sp, message(23, at))
	require.Equal(t, int64(1), pool.begins.Load())
	api := sp.Api.(*model.Api)
	var row model.DeviceState
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "d").First(&row).Error)
	require.True(t, row.Incarnation.Valid)
	require.Equal(t, int64(23), row.Incarnation.Int64)
	before := dump(t, sp)
	// A late event from 17 has a newer clock and new reading timestamps. It must
	// change neither the parent nor the children even on the single-message path.
	seed(t, sp, message(17, at.Add(time.Hour)))
	require.Equal(t, before, dump(t, sp))
	require.Equal(t, int64(2), pool.begins.Load())
}
