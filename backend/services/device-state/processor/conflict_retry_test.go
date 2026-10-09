// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/jackc/pgx/v5/pgconn"
)

// conflictApi is the real Api over sqlite, except that a device's first `conflicts` per-event
// device-state writes fail the way a deadlock victim's does (SQLSTATE 40P01).
type conflictApi struct {
	*model.Api
	err       error
	conflicts int32
	calls     atomic.Int32
}

func (c *conflictApi) MergeDeviceState(ctx context.Context, token string, at time.Time, pt *model.PresenceTransition,
	id model.DeviceIdentity) (*model.DeviceState, error) {
	if c.calls.Add(1) <= c.conflicts {
		return nil, c.err
	}
	return c.Api.MergeDeviceState(ctx, token, at, pt, id)
}

// A per-event write that loses a deadlock rolled back completely, so it is run again in place
// and the event is acknowledged once, without waiting a full AckWait for redelivery. A conflict
// that outlasts the attempts, or an error that is not a conflict, is not retried in place: the
// event is left unacknowledged for redelivery, as before.
func TestAConflictOnTheSingleWriteIsRetriedInPlace(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	for _, tc := range []struct {
		name       string
		err        error
		conflicts  int32
		wantCalls  int32
		wantAcked  int32
		wantResult string
	}{
		{"one deadlock is retried and the event is acked once", deadlock, 1, 2, 1, core.ResultOK},
		{"a serialization failure is retried too", &pgconn.PgError{Code: "40001"}, 2, 3, 1, core.ResultOK},
		{"a conflict that outlasts the attempts is left for redelivery", deadlock, 10, conflictAttempts, 0, core.ResultRetry},
		{"an error that is not a conflict is not retried", errors.New("refused"), 1, 1, 0, core.ResultRetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, _, _ := newFencedStateProcessor(t)
			api := &conflictApi{Api: built.Api.(*model.Api), err: tc.err, conflicts: tc.conflicts}
			built.Api = api
			reg := stateRegistry(built)

			acks := drainQueue(built, []messaging.Message{threeMetrics(t, "cr-a", t0)})
			built.Api = api.Api // the loaders below read through the real Api

			if got := api.calls.Load(); got != tc.wantCalls {
				t.Errorf("MergeDeviceState ran %d times; want %d", got, tc.wantCalls)
			}
			if got := ackCounts(acks)[0]; got != tc.wantAcked {
				t.Errorf("event acknowledged %d times; want %d", got, tc.wantAcked)
			}
			if v, _, _, _ := gathered(t, reg, "state_messages_total", tc.wantResult); v != 1 {
				t.Errorf("state_messages_total{result=%s} = %v; want 1", tc.wantResult, v)
			}
			if tc.wantAcked == 1 {
				assertProjectedAt(t, built, "cr-a", t0)
			}
		})
	}
}
