// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// A batch of ONE event is one transaction, not two. The per-message path opens a transaction
// for the device state and another for the latest values; the batch path folds both into one,
// and a lone event used to be sent down the per-message path regardless.

// txCountingPool counts the transactions gorm opens: the statement counter does not see
// BEGIN, which is the thing being measured here.
type txCountingPool struct {
	gorm.ConnPool
	begins atomic.Int64
}

func (p *txCountingPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	p.begins.Add(1)
	return p.ConnPool.(gorm.ConnPoolBeginner).BeginTx(ctx, opts)
}

func (p *txCountingPool) GetDBConn() (*sql.DB, error) {
	return p.ConnPool.(gorm.GetDBConnector).GetDBConn()
}

// countTransactions makes db count every transaction it opens from now on.
func countTransactions(db *gorm.DB) *txCountingPool {
	pool := &txCountingPool{ConnPool: db.ConnPool}
	db.ConnPool = pool
	db.Statement.ConnPool = pool
	return pool
}

func stateChange(t *testing.T, device, state string, session uint64, at time.Time) messaging.Message {
	t.Helper()
	event := &dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: device,
		EventType:         esmodel.StateChange,
		OccurredTime:      at,
		ProcessedTime:     at,
		Payload:           &dmmodel.ResolvedStateChangePayload{State: state, SessionId: session},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.Message{Subject: locationTestSubject, Value: encoded}
}

// A single-event batch commits in ONE transaction; merged by the per-message path it is two.
func TestABatchOfOneEventIsOneTransaction(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		msg  func(t *testing.T) messaging.Message
	}{
		{"measurement", func(t *testing.T) messaging.Message { return threeMetrics(t, "tx-01", t0) }},
		{"location", func(t *testing.T) messaging.Message { return aFix(t, "tx-01", t0, "28.5") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, db, _ := newFencedStateProcessor(t)
			pool := countTransactions(db)
			acks := drainQueue(sp, []messaging.Message{tc.msg(t)})
			if got := pool.begins.Load(); got != 1 {
				t.Errorf("a single-event batch opened %d transactions; want 1", got)
			}
			if got := ackCounts(acks)[0]; got != 1 {
				t.Errorf("event acknowledged %d times; want 1", got)
			}
		})
	}

	// The control: the per-message path really is two, so the count above can tell them apart.
	t.Run("per-message path control", func(t *testing.T) {
		sp, db, _ := newFencedStateProcessor(t)
		pool := countTransactions(db)
		seed(t, sp, threeMetrics(t, "tx-01", t0))
		if got := pool.begins.Load(); got != 2 {
			t.Errorf("the per-message path opened %d transactions; want 2", got)
		}
	})
}

// dump is every projection row the tenant holds, minus the columns that only say when and in
// which order rows were written.
func dump(t *testing.T, sp *StateProcessor) []map[string]any {
	t.Helper()
	ctx := core.WithTenant(context.Background(), fenceTenant)
	api := sp.Api.(*model.Api)
	var out []map[string]any
	add := func(rows any) {
		raw, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var maps []map[string]any
		if err := json.Unmarshal(raw, &maps); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, m := range maps {
			for _, k := range []string{"ID", "CreatedAt", "UpdatedAt", "DeletedAt"} {
				delete(m, k)
			}
			out = append(out, m)
		}
	}
	var states []model.DeviceState
	var measurements []model.LatestMeasurement
	var locations []model.LatestLocation
	if err := api.RDB.DB(ctx).Order("device_token").Find(&states).Error; err != nil {
		t.Fatalf("load states: %v", err)
	}
	if err := api.RDB.DB(ctx).Order("device_token, name").Find(&measurements).Error; err != nil {
		t.Fatalf("load measurements: %v", err)
	}
	if err := api.RDB.DB(ctx).Order("device_token").Find(&locations).Error; err != nil {
		t.Fatalf("load locations: %v", err)
	}
	add(states)
	add(measurements)
	add(locations)
	return out
}

// The projection a lone event leaves behind, and how it is disposed of, are the same whether
// it takes the one-transaction batch path or the two-transaction per-message path.
func TestABatchOfOneLeavesTheProjectionThePerMessagePathDid(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	cases := []struct {
		name  string
		fence bool // plant an erasure fence for the tenant before the event under test
		seed  func(t *testing.T) []messaging.Message
		msg   func(t *testing.T) messaging.Message
	}{
		{"measurement, first sight", false, nil,
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t1) }},
		{"measurement, device seen before", false,
			func(t *testing.T) []messaging.Message { return []messaging.Message{threeMetrics(t, "eq-01", t0)} },
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t1) }},
		{"location, first sight", false, nil,
			func(t *testing.T) messaging.Message { return aFix(t, "eq-01", t1, "28.5") }},
		{"location, newer fix", false,
			func(t *testing.T) []messaging.Message { return []messaging.Message{aFix(t, "eq-01", t0, "10.25")} },
			func(t *testing.T) messaging.Message { return aFix(t, "eq-01", t1, "28.5") }},
		{"presence connect", false, nil,
			func(t *testing.T) messaging.Message { return stateChange(t, "eq-01", "CONNECTED", 5, t1) }},
		{"presence disconnect of a connected device", false,
			func(t *testing.T) []messaging.Message {
				return []messaging.Message{stateChange(t, "eq-01", "CONNECTED", 5, t0)}
			},
			func(t *testing.T) messaging.Message { return stateChange(t, "eq-01", "DISCONNECTED", 5, t1) }},
		{"stale measurement (older than stored)", false,
			func(t *testing.T) []messaging.Message { return []messaging.Message{threeMetrics(t, "eq-01", t2)} },
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t0) }},
		{"stale location (older than stored)", false,
			func(t *testing.T) []messaging.Message { return []messaging.Message{aFix(t, "eq-01", t2, "10.25")} },
			func(t *testing.T) messaging.Message { return aFix(t, "eq-01", t0, "28.5") }},
		{"duplicate redelivery", false,
			func(t *testing.T) []messaging.Message { return []messaging.Message{threeMetrics(t, "eq-01", t1)} },
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t1) }},
		{"erased (fenced) tenant, first sight", true, nil,
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t1) }},
		{"erased (fenced) tenant, device seen before", true,
			func(t *testing.T) []messaging.Message { return []messaging.Message{threeMetrics(t, "eq-01", t0)} },
			func(t *testing.T) messaging.Message { return threeMetrics(t, "eq-01", t1) }},
	}
	type outcome struct {
		Rows            []map[string]any
		Acked           int32
		Ok, Retry, Drop float64
	}
	run := func(t *testing.T, c int, batch bool) outcome {
		tc := cases[c]
		sp, db, _ := newFencedStateProcessor(t)
		if tc.seed != nil {
			for _, m := range tc.seed(t) {
				seed(t, sp, m)
			}
		}
		if tc.fence {
			plantFence(t, db, fenceTenant)
		}
		reg := stateRegistry(sp)
		var acked int32
		if batch {
			acked = ackCounts(drainQueue(sp, []messaging.Message{tc.msg(t)}))[0]
		} else {
			ack := &recordingAck{}
			sp.mergeOne(context.Background(), consumed(tc.msg(t), ack))
			acked = ack.n.Load()
		}
		o := outcome{Rows: dump(t, sp), Acked: acked}
		o.Ok, _, _, _ = gathered(t, reg, "state_messages_total", core.ResultOK)
		o.Retry, _, _, _ = gathered(t, reg, "state_messages_total", core.ResultRetry)
		o.Drop, _, _, _ = gathered(t, reg, "state_messages_total", core.ResultDropped)
		return o
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var old, got outcome
			t.Run("per-message", func(t *testing.T) { old = run(t, i, false) })
			t.Run("batch-of-one", func(t *testing.T) { got = run(t, i, true) })
			if !reflect.DeepEqual(old, got) {
				t.Errorf("a batch of one diverged from the per-message path:\n per-message: %+v\n batch of one: %+v", old, got)
			}
			if tc.fence && (got.Acked != 0 || got.Retry != 1) {
				t.Errorf("an event for a fenced tenant was acked %d times with retry=%v; want it left for redelivery", got.Acked, got.Retry)
			}
			if !tc.fence && got.Acked != 1 {
				t.Errorf("event acknowledged %d times; want 1", got.Acked)
			}
		})
	}
}

// batchFailApi fails MergeProjectionBatch with err and counts the per-message state writes
// made afterwards.
type batchFailApi struct {
	*model.Api
	err      error
	batches  atomic.Int32
	perState atomic.Int32
}

func (b *batchFailApi) MergeProjectionBatch(context.Context, []model.ProjectionUpdate) error {
	b.batches.Add(1)
	return b.err
}

func (b *batchFailApi) MergeDeviceState(ctx context.Context, token string, at time.Time, pt *model.PresenceTransition,
	id model.DeviceIdentity) (*model.DeviceState, error) {
	b.perState.Add(1)
	return b.Api.MergeDeviceState(ctx, token, at, pt, id)
}

// When the one-transaction merge of a lone event fails, the per-message path runs as it always
// has, and the event is still merged and acknowledged once.
func TestABatchOfOneThatFailsFallsBackToThePerMessagePath(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a conflict", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}},
		{"another failure", errors.New("commit failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, _, _ := newFencedStateProcessor(t)
			api := &batchFailApi{Api: built.Api.(*model.Api), err: tc.err}
			built.Api = api
			reg := stateRegistry(built)
			acks := drainQueue(built, []messaging.Message{threeMetrics(t, "bc-01", t0)})
			built.Api = api.Api
			if got := api.batches.Load(); got != 1 {
				t.Errorf("MergeProjectionBatch ran %d times; want 1", got)
			}
			if got := api.perState.Load(); got != 1 {
				t.Errorf("the per-message path ran %d times; want 1", got)
			}
			if got := ackCounts(acks)[0]; got != 1 {
				t.Errorf("event acknowledged %d times; want 1", got)
			}
			assertProjectedAt(t, built, "bc-01", t0)
			if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultOK); v != 1 {
				t.Errorf("state_messages_total{result=ok} = %v; want 1", v)
			}
			if v, _, _, _ := gathered(t, reg, "state_batch_fallbacks_total", ""); v != 1 {
				t.Errorf("state_batch_fallbacks_total = %v; want 1", v)
			}
		})
	}
}
