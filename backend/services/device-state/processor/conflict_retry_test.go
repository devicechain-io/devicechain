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

// conflictApi is the real Api over sqlite, except that the first `conflicts` calls of the
// write named by `target` fail the way a deadlock victim's do (SQLSTATE 40P01). Each of the
// three per-event writes is wrapped, because each is retried in place on its own.
type conflictApi struct {
	*model.Api
	target    string // "state", "measurements" or "locations"
	err       error
	conflicts int32
	calls     atomic.Int32
}

func (c *conflictApi) fails(which string) bool {
	return which == c.target && c.calls.Add(1) <= c.conflicts
}

func (c *conflictApi) MergeDeviceState(ctx context.Context, token string, at time.Time, pt *model.PresenceTransition,
	id model.DeviceIdentity) (*model.DeviceState, error) {
	if c.fails("state") {
		return nil, c.err
	}
	return c.Api.MergeDeviceState(ctx, token, at, pt, id)
}

func (c *conflictApi) MergeLatestMeasurements(ctx context.Context, token string, in []model.LatestMeasurementInput) error {
	if c.fails("measurements") {
		return c.err
	}
	return c.Api.MergeLatestMeasurements(ctx, token, in)
}

func (c *conflictApi) MergeLatestLocations(ctx context.Context, token string, in []model.LatestLocationInput) error {
	if c.fails("locations") {
		return c.err
	}
	return c.Api.MergeLatestLocations(ctx, token, in)
}

// A per-event write that loses a deadlock rolled back completely, so it is run again in place
// and the event is acknowledged once, without waiting a full AckWait for redelivery. A conflict
// that outlasts the attempts, or an error that is not a conflict, is not retried in place: the
// event is left unacknowledged for redelivery, as before. This holds for each of the three
// writes an event can make (device state, latest measurements, latest location), and every
// in-place retry is counted in state_write_conflict_retries_total.
func TestAConflictOnAPerEventWriteIsRetriedInPlace(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	for _, target := range []string{"state", "measurements", "locations"} {
		for _, tc := range []struct {
			name        string
			err         error
			conflicts   int32
			wantCalls   int32
			wantAcked   int32
			wantResult  string
			wantRetries float64
		}{
			{"one deadlock is retried and the event is acked once", deadlock, 1, 2, 1, core.ResultOK, 1},
			{"a serialization failure is retried too", &pgconn.PgError{Code: "40001"}, 2, 3, 1, core.ResultOK, 2},
			{"a conflict that outlasts the attempts is left for redelivery", deadlock, 10, conflictAttempts, 0, core.ResultRetry, conflictAttempts - 1},
			{"an error that is not a conflict is not retried", errors.New("refused"), 1, 1, 0, core.ResultRetry, 0},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				built, _, _ := newFencedStateProcessor(t)
				api := &conflictApi{Api: built.Api.(*model.Api), target: target, err: tc.err, conflicts: tc.conflicts}
				built.Api = api
				reg := stateRegistry(built)

				msg := threeMetrics(t, "cr-a", t0)
				if target == "locations" {
					msg = aFix(t, "cr-a", t0, "40.5")
				}
				acks := drainQueue(built, []messaging.Message{msg})
				built.Api = api.Api // the loaders below read through the real Api

				if got := api.calls.Load(); got != tc.wantCalls {
					t.Errorf("the %s write ran %d times; want %d", target, got, tc.wantCalls)
				}
				if got := ackCounts(acks)[0]; got != tc.wantAcked {
					t.Errorf("event acknowledged %d times; want %d", got, tc.wantAcked)
				}
				if v, _, _, _ := gathered(t, reg, "state_messages_total", tc.wantResult); v != 1 {
					t.Errorf("state_messages_total{result=%s} = %v; want 1", tc.wantResult, v)
				}
				if v, _, _, _ := gathered(t, reg, "state_write_conflict_retries_total", ""); v != tc.wantRetries {
					t.Errorf("state_write_conflict_retries_total = %v; want %v", v, tc.wantRetries)
				}
				if tc.wantAcked == 1 {
					ctx := core.WithTenant(context.Background(), fenceTenant)
					if target == "locations" {
						loadProjectedLocation(t, built, ctx, "cr-a")
					} else {
						assertProjectedAt(t, built, "cr-a", t0)
					}
				}
			})
		}
	}
}

// A write that lost a conflict is not run again once the processor's context is done: the
// pause between attempts ends at once, and the conflict goes to the caller untouched.
func TestARetryPauseEndsWhenTheContextIsDone(t *testing.T) {
	deadlock := &pgconn.PgError{Code: "40P01"}
	sp := &StateProcessor{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	err := sp.retryOnConflict(ctx, func() error { calls++; return deadlock })
	if calls != 1 {
		t.Errorf("the write ran %d times after the context was cancelled; want 1", calls)
	}
	if !errors.Is(err, deadlock) {
		t.Errorf("returned %v; want the conflict itself", err)
	}
}
