// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A message whose write is refused must not fail its batch-mates, and must itself be
// disposed of exactly as it was before batching: retried while it has deliveries left, then
// dropped.

var errPoison = errors.New("poison write refused")

// poisonApi is the real Api over sqlite, except that any write touching device "poison" is
// refused: a batch holding it fails for the poison's tenant, as a real refused statement
// does, and the poison's own per-message merge fails too.
type poisonApi struct {
	*model.Api
	batches int
}

func (p *poisonApi) MergeProjectionBatch(ctx context.Context, updates []model.ProjectionUpdate) error {
	p.batches++
	for _, u := range updates {
		if u.DeviceToken == "poison" {
			return &model.BatchRefusal{Tenant: u.Tenant, Err: errPoison}
		}
	}
	return p.Api.MergeProjectionBatch(ctx, updates)
}

func (p *poisonApi) MergeDeviceState(ctx context.Context, token string, at time.Time, pt *model.PresenceTransition,
	id model.DeviceIdentity) (*model.DeviceState, error) {
	if token == "poison" {
		return nil, errPoison
	}
	return p.Api.MergeDeviceState(ctx, token, at, pt, id)
}

func TestAPoisonWriteDoesNotFailItsBatchMates(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		delivered int
		// the poison's disposition: acked or not, and the result it is counted under.
		wantAcked  int32
		wantResult string
	}{
		{"with deliveries left, it is left for redelivery", 1, 0, core.ResultRetry},
		{"on its last delivery, it is dropped", messaging.MaxDeliver, 1, core.ResultDropped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, _, _ := newFencedStateProcessor(t)
			api := &poisonApi{Api: built.Api.(*model.Api)}
			built.Api = api
			reg := stateRegistry(built)

			msgs := []messaging.Message{
				threeMetrics(t, "pa", t0),
				threeMetrics(t, "poison", t0),
				threeMetrics(t, "pb", t0),
				forTenant(threeMetrics(t, "pc", t0), otherTenant),
			}
			ch := make(chan messaging.Message, len(msgs))
			acks := make([]*recordingAck, len(msgs))
			for i, m := range msgs {
				acks[i] = &recordingAck{}
				delivered := 1
				if i == 1 {
					delivered = tc.delivered
				}
				ch <- messaging.NewConsumedMessage(m.Subject, m.Value, delivered, nil, acks[i])
			}
			close(ch)
			built.messages = ch
			built.processMessages(context.Background())
			built.Api = api.Api // the loaders below read through the real Api

			got := ackCounts(acks)
			for i, want := range []int32{1, tc.wantAcked, 1, 1} {
				if got[i] != want {
					t.Errorf("message %d acknowledged %d times; want %d", i, got[i], want)
				}
			}
			assertProjectedAt(t, built, "pa", t0)
			assertProjectedAt(t, built, "pb", t0)
			assertProjectedAtFor(t, built, otherTenant, "pc", t0)
			if v, _, _, _ := gathered(t, reg, "state_messages_total", tc.wantResult); v != 1 {
				t.Errorf("state_messages_total{result=%s} = %v; want 1 (the poison)", tc.wantResult, v)
			}
			if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultOK); v != 3 {
				t.Errorf("state_messages_total{result=ok} = %v; want 3", v)
			}
			// The first batch was refused for tenant1; tenant1's three went one at a time, and
			// tenant2's one remaining message is a batch of one, merged on its own. So one
			// batch attempt and one fallback.
			if api.batches != 1 {
				t.Errorf("MergeProjectionBatch ran %d times; want 1", api.batches)
			}
			if v, _, _, _ := gathered(t, reg, "state_batch_fallbacks_total", ""); v != 1 {
				t.Errorf("state_batch_fallbacks_total = %v; want 1", v)
			}
		})
	}
}

// Messages that cannot be admitted — no parseable tenant, an undecodable event — are acked
// as invalid where they stand and join no batch: the rest commit in ONE transaction, with no
// fallback.
func TestInvalidMessagesInAQueueDoNotBreakItsBatch(t *testing.T) {
	sp, _, _ := newFencedStateProcessor(t)
	reg := stateRegistry(sp)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	good := threeMetrics(t, "iv-a", t0)
	acks := drainQueue(sp, []messaging.Message{
		good,
		{Subject: "no-tenant-here", Value: good.Value},
		threeMetrics(t, "iv-b", t0),
		{Subject: locationTestSubject, Value: []byte("not a resolved event")},
		threeMetrics(t, "iv-c", t0),
	})
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1 (merged, or dropped as invalid)", i, n)
		}
	}
	for _, d := range []string{"iv-a", "iv-b", "iv-c"} {
		assertProjectedAt(t, sp, d, t0)
	}
	if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultInvalid); v != 2 {
		t.Errorf("state_messages_total{result=invalid} = %v; want 2", v)
	}
	if _, n, sum, _ := gathered(t, reg, "state_batch_size", ""); n != 1 || sum != 3 {
		t.Errorf("batch-size histogram holds %v observations summing to %v; want ONE batch of 3", n, sum)
	}
	if v, _, _, found := gathered(t, reg, "state_batch_fallbacks_total", ""); !found || v != 0 {
		t.Errorf("state_batch_fallbacks_total = %v (found=%v); want 0", v, found)
	}
}

// A failure no tenant is blamed for — here, an error that is not a BatchRefusal, which is
// what a failed BEGIN or COMMIT returns — merges every message again on its own.
func TestABatchThatCannotCommitMergesEveryEventOnItsOwn(t *testing.T) {
	built, _, _ := newFencedStateProcessor(t)
	api := &commitFailApi{Api: built.Api.(*model.Api)}
	built.Api = api
	reg := stateRegistry(built)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	acks := drainQueue(built, []messaging.Message{
		threeMetrics(t, "cf-a", t0), forTenant(threeMetrics(t, "cf-b", t0), otherTenant), threeMetrics(t, "cf-c", t0),
	})
	built.Api = api.Api // the loaders below read through the real Api
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	assertProjectedAt(t, built, "cf-a", t0)
	assertProjectedAtFor(t, built, otherTenant, "cf-b", t0)
	assertProjectedAt(t, built, "cf-c", t0)
	if api.batches != 1 {
		t.Errorf("MergeProjectionBatch ran %d times; want 1 (then every event on its own)", api.batches)
	}
	if _, n, sum, _ := gathered(t, reg, "state_batch_size", ""); n != 3 || sum != 3 {
		t.Errorf("batch-size histogram holds %v observations summing to %v; want three commits of one", n, sum)
	}
}

// commitFailApi fails every batch the way a failed COMMIT does: with an error that blames no
// tenant.
type commitFailApi struct {
	*model.Api
	batches int
}

func (c *commitFailApi) MergeProjectionBatch(context.Context, []model.ProjectionUpdate) error {
	c.batches++
	return errors.New("commit failed")
}
