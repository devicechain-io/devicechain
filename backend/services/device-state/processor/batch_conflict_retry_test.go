// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A batch that loses a transaction conflict (a deadlock victim, a serialization failure) was
// rolled back whole and is run again ONCE, unchanged: one extra transaction instead of the
// per-message path's two to four per event. Anything else takes the branches it always did.

// scriptedApi is the real Api over sqlite, except that MergeProjectionBatch answers each
// call with the next scripted error (then runs for real), and records what every call was
// given and how many per-message merges ran.
type scriptedApi struct {
	*model.Api
	script  []error
	calls   [][]model.ProjectionUpdate
	perMsg  int
	tenant  string
	refusal bool // wrap each scripted error in a BatchRefusal for tenant
}

func (a *scriptedApi) MergeProjectionBatch(ctx context.Context, updates []model.ProjectionUpdate) error {
	a.calls = append(a.calls, append([]model.ProjectionUpdate(nil), updates...))
	if n := len(a.calls) - 1; n < len(a.script) && a.script[n] != nil {
		if a.refusal {
			return &model.BatchRefusal{Tenant: a.tenant, Err: a.script[n]}
		}
		return a.script[n]
	}
	return a.Api.MergeProjectionBatch(ctx, updates)
}

func (a *scriptedApi) MergeDeviceState(ctx context.Context, token string, at time.Time, pt *model.PresenceTransition,
	id model.DeviceIdentity) (*model.DeviceState, error) {
	a.perMsg++
	return a.Api.MergeDeviceState(ctx, token, at, pt, id)
}

var (
	deadlock = &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	serial   = &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
)

func runScripted(t *testing.T, script []error, refusal bool) (*scriptedApi, *StateProcessor, []*recordingAck) {
	t.Helper()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	built, _, _ := newFencedStateProcessor(t)
	api := &scriptedApi{Api: built.Api.(*model.Api), script: script, tenant: "tenant1", refusal: refusal}
	built.Api = api
	// Tokens deliberately out of order: the retry must pass them in the order they came.
	tokens := []string{"rc-b", "rc-a", "rc-d", "rc-c"}
	ch := make(chan messaging.Message, len(tokens))
	acks := make([]*recordingAck, len(tokens))
	for i, tok := range tokens {
		m := threeMetrics(t, tok, t0)
		acks[i] = &recordingAck{}
		ch <- messaging.NewConsumedMessage(m.Subject, m.Value, 1, nil, acks[i])
	}
	close(ch)
	built.messages = ch
	built.processMessages(context.Background())
	return api, built, acks
}

func allAcked(t *testing.T, acks []*recordingAck) {
	t.Helper()
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
}

// One conflict, then success: the same batch, in the same order, commits once with no
// fallback and no per-message merge.
func TestABatchThatLosesAConflictIsRunAgainOnceUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"deadlock", deadlock}, {"serialization failure", serial}} {
		t.Run(tc.name, func(t *testing.T) {
			api, sp, acks := runScripted(t, []error{tc.err}, true)
			if len(api.calls) != 2 {
				t.Fatalf("the batch was merged %d times; want 2 (the loss and one retry)", len(api.calls))
			}
			first, second := api.calls[0], api.calls[1]
			if len(first) != 4 || len(second) != 4 {
				t.Fatalf("batches of %d and %d; want 4 both times", len(first), len(second))
			}
			for i := range first {
				if first[i].DeviceToken != second[i].DeviceToken {
					t.Errorf("update %d: %s then %s; the retry must pass the same batch in the same order",
						i, first[i].DeviceToken, second[i].DeviceToken)
				}
			}
			if got := testutil.ToFloat64(sp.metrics.batchConflictRetries); got != 1 {
				t.Errorf("state_batch_conflict_retries_total = %v; want 1", got)
			}
			if got := testutil.ToFloat64(sp.metrics.fallbacks); got != 0 {
				t.Errorf("state_batch_fallbacks_total = %v; want 0: the retry committed", got)
			}
			if api.perMsg != 0 {
				t.Errorf("%d per-message merges; want none", api.perMsg)
			}
			allAcked(t, acks)
		})
	}
}

// A second conflict is not retried again: the batch takes the per-message path, one fallback.
func TestABatchThatLosesTwoConflictsFallsBackToPerMessage(t *testing.T) {
	api, sp, acks := runScripted(t, []error{deadlock, deadlock}, true)
	if len(api.calls) != 2 {
		t.Errorf("the batch was merged %d times; want 2 (never a third)", len(api.calls))
	}
	if got := testutil.ToFloat64(sp.metrics.batchConflictRetries); got != 1 {
		t.Errorf("state_batch_conflict_retries_total = %v; want 1", got)
	}
	if got := testutil.ToFloat64(sp.metrics.fallbacks); got != 1 {
		t.Errorf("state_batch_fallbacks_total = %v; want 1", got)
	}
	if api.perMsg != 4 {
		t.Errorf("%d per-message merges; want 4", api.perMsg)
	}
	allAcked(t, acks)
}

// Nothing but a conflict is retried: a refused statement, a fence refusal and a lost
// connection each take today's branch at once.
func TestOnlyAConflictIsRetried(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		refusal    bool
		wantPerMsg int
	}{
		{"a refused statement", errPoison, true, 4},
		{"a fence refusal", rdb.ErrTenantPurged, true, 0},
		{"a lost connection", &pgconn.PgError{Code: "08006", Message: "connection failure"}, false, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, sp, _ := runScripted(t, []error{tc.err}, tc.refusal)
			if len(api.calls) != 1 {
				t.Errorf("the batch was merged %d times; want 1 (no retry)", len(api.calls))
			}
			if got := testutil.ToFloat64(sp.metrics.batchConflictRetries); got != 0 {
				t.Errorf("state_batch_conflict_retries_total = %v; want 0", got)
			}
			if got := testutil.ToFloat64(sp.metrics.fallbacks); got != 1 {
				t.Errorf("state_batch_fallbacks_total = %v; want 1", got)
			}
			if api.perMsg != tc.wantPerMsg {
				t.Errorf("%d per-message merges; want %d", api.perMsg, tc.wantPerMsg)
			}
		})
	}
}
