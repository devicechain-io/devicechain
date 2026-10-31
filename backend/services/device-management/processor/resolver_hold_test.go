// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// holdingResolver rebuilds the suite's processor with a store hold over store, so the resolver
// pool is started the way the service starts it.
func (suite *InboundEventsProcessorTestSuite) holdingResolver(store *holdtest.Store) {
	suite.SetupTest()
	suite.IP.hold = &storehold.Holder{Name: "inbound-event-resolution", Probe: store.Probe, Tune: holdtest.FastTuning()}
	suite.Require().NoError(suite.IP.initializeEventResolvers(context.Background()))
}

// THE REGRESSION: a database outage longer than the delivery budget used to dead-letter and ack
// an event the device had already been PUBACKed for. Here the event arrives on its LAST delivery
// and the store refuses the lookup; it must be HELD (not reported, not acked) and resolved once
// the store answers. The counterweight (no hold) shows the same event lettered, so the first
// half cannot pass on a path that never letters anything.
func (suite *InboundEventsProcessorTestSuite) TestAnEventResolvedDuringAStoreOutageIsHeldNotLettered() {
	for _, withHold := range []bool{true, false} {
		name := "held"
		if !withHold {
			name = "control: no hold letters it"
		}
		suite.Run(name, func() {
			store := &holdtest.Store{}
			suite.SetupTest()
			if withHold {
				suite.holdingResolver(store)
			}
			bytes, err := esproto.MarshalUnresolvedEvent(buildNewAssignmentEvent())
			suite.Require().NoError(err)
			ack := &atomicAck{}
			msg := messaging.NewConsumedMessage(testTenantSubject, bytes, messaging.MaxDeliver, nil, ack)
			var lookups atomic.Int32
			suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
			suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return(
				[]*dmodel.Device(nil), holdtest.DiskFull).Run(func(mock.Arguments) { lookups.Add(1) }).Times(3)
			suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{buildDevice()}, nil).
				Run(func(mock.Arguments) { lookups.Add(1) })
			suite.API.Mock.On("TrackedRelationshipsForDevice", mock.Anything, mock.Anything).Return(
				&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)
			suite.API.Mock.On("EntityRelationshipsByToken", mock.Anything, mock.Anything).Return([]*dmodel.EntityRelationship{}, nil)
			suite.API.Mock.On("CreateEntityRelationship", mock.Anything, mock.Anything).Return(
				&dmodel.EntityRelationship{}, nil)

			readAndHandleOne(suite.IP, context.Background())

			if !withHold {
				select {
				case <-suite.IP.failed:
				case <-time.After(2 * time.Second):
					suite.Fail("control: without a hold the event should have been lettered at the cap")
				}
				return
			}
			select {
			case item := <-suite.IP.failed:
				suite.Failf("lettered", "an event refused by a full store was routed to failed-events: %+v", item.event)
			case <-time.After(500 * time.Millisecond):
			}
			suite.Equal(int32(0), ack.n.Load(), "a held event must stay unacked")
			suite.Eventually(func() bool { return lookups.Load() >= 4 },
				5*time.Second, 10*time.Millisecond, "the event was not resolved again after the outage")
			suite.Equal(0, len(suite.IP.failed), "nothing may be lettered")
			// Exactly one resolved event comes out, and settling it (what the publisher does
			// once the broker has stored it) is what acks the source, exactly once.
			select {
			case item := <-suite.IP.resolved:
				suite.Equal(int32(0), ack.n.Load(), "the source is acked only after its resolved event is published")
				suite.IP.settleResolved(item.coord, nil)
			case <-time.After(5 * time.Second):
				suite.FailNow("the held event was never resolved")
			}
			suite.Eventually(func() bool { return ack.n.Load() == 1 }, 2*time.Second, 5*time.Millisecond, "the source was not acked after recovery")
			select {
			case <-suite.IP.resolved:
				suite.Fail("the held event produced a second resolved event")
			case <-time.After(300 * time.Millisecond):
			}
			suite.Equal(int32(1), ack.n.Load(), "acked exactly once")
		})
	}
}

// A connection-class error with a LIVE store is a payload that crashes its backend, not an
// outage: it takes its delivery budget and is lettered at the cap as before.
func (suite *InboundEventsProcessorTestSuite) TestAPoisonEventWithALiveStoreStillTakesItsDeliveryBudget() {
	store := &holdtest.Store{} // up: the probe succeeds
	suite.holdingResolver(store)
	bytes, err := esproto.MarshalUnresolvedEvent(buildNewAssignmentEvent())
	suite.Require().NoError(err)
	msg := messaging.NewConsumedMessage(testTenantSubject, bytes, messaging.MaxDeliver, nil, &atomicAck{})
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device(nil), io.ErrUnexpectedEOF)

	readAndHandleOne(suite.IP, context.Background())

	select {
	case <-suite.IP.failed:
	case <-time.After(2 * time.Second):
		suite.Fail("a poison event (store answers the probe) must be lettered at its delivery cap")
	}
}

// relationshipApi records the tokens the resolver looks up and creates relationships under, and
// answers a lookup with what was created, like the real store.
type relationshipApi struct {
	*dmtest.MockApi
	created []*dmodel.EntityRelationship
	looked  []string
	creates []string
}

func (a *relationshipApi) EntityRelationshipsByToken(_ context.Context, tokens []string) ([]*dmodel.EntityRelationship, error) {
	a.looked = append(a.looked, tokens...)
	var out []*dmodel.EntityRelationship
	for _, c := range a.created {
		if c.Token == tokens[0] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (a *relationshipApi) CreateEntityRelationship(_ context.Context, r *dmodel.EntityRelationshipCreateRequest) (*dmodel.EntityRelationship, error) {
	a.creates = append(a.creates, r.Token)
	rel := &dmodel.EntityRelationship{TargetType: r.TargetType, TargetToken: r.Target, TargetId: 7}
	rel.Token = r.Token
	a.created = append(a.created, rel)
	return rel, nil
}

// A relationship created for an event is found again, by its derived token, when the event is
// re-run after a commit whose acknowledgement was lost: the second pass must not create it again.
func TestARerunNewRelationshipEventReusesTheOneItCreated(t *testing.T) {
	api := &relationshipApi{MockApi: &dmtest.MockApi{}}
	rez := &EventResolver{Api: api}
	device := buildDevice()
	payload := esmodel.UnresolvedNewRelationshipPayload{RelationshipType: "controls", TargetType: "asset", Target: "car123"}
	ctx := core.WithTenant(context.Background(), "acme")

	first, _, err := rez.CreateNewEntityRelationship(ctx, device, payload)
	require.NoError(t, err)
	second, _, err := rez.CreateNewEntityRelationship(ctx, device, payload)
	require.NoError(t, err)
	require.Same(t, first, second, "the re-run must reuse the relationship the first pass created")
	require.Len(t, api.creates, 1, "created exactly once")
	require.Equal(t, api.looked[0], api.creates[0], "the lookup and the create agree on the derived token")
	require.Equal(t, api.looked[0], api.looked[1], "and a re-run derives the same one")

	other := esmodel.UnresolvedNewRelationshipPayload{RelationshipType: "controls", TargetType: "asset", Target: "car999"}
	_, _, err = rez.CreateNewEntityRelationship(ctx, device, other)
	require.NoError(t, err)
	require.Len(t, api.creates, 2, "a different target is a different relationship")
}
