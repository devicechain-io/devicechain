// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/mock"
)

// atomicAck counts acks from the resolver's own goroutine.
type atomicAck struct{ n atomic.Int32 }

func (a *atomicAck) Ack() error { a.n.Add(1); return nil }

// A relationship event whose write the erasure fence refuses is settled where it is found and
// reported NOWHERE — on its FINAL delivery too, which is where a transient failure is
// published to failed-events. A failed-events copy of a deleted tenant's event would be a new
// record of the data being erased. The counterweight is the same event failing for any other
// reason at the cap: it IS reported, so the first half cannot pass on a path that never
// reports anything.
func (suite *InboundEventsProcessorTestSuite) TestARelationshipEventRefusedByTheFenceIsSettledNotReported() {
	for _, tc := range []struct {
		name     string
		err      error
		reported bool
	}{
		{"refused by the fence", fmt.Errorf("creating: %w", rdb.ErrTenantPurged), false},
		{"control: any other failure", errors.New("database down"), true},
	} {
		suite.Run(tc.name, func() {
			suite.SetupTest()
			reg := suite.withRegistry()
			bytes, err := esproto.MarshalUnresolvedEvent(buildNewAssignmentEvent())
			suite.Require().NoError(err)
			ack := &atomicAck{}
			msg := messaging.NewConsumedMessage(testTenantSubject, bytes, messaging.MaxDeliver, nil, ack)

			suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
			suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{buildDevice()}, nil)
			suite.API.Mock.On("TrackedRelationshipsForDevice", mock.Anything, mock.Anything).Return(
				&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)
			suite.API.Mock.On("EntityRelationshipsByToken", mock.Anything, mock.Anything).Return([]*dmodel.EntityRelationship{}, nil)
			suite.API.Mock.On("CreateEntityRelationship", mock.Anything, mock.Anything).Return(
				(*dmodel.EntityRelationship)(nil), tc.err)

			readAndHandleOne(suite.IP, context.Background())

			select {
			case item := <-suite.IP.failed:
				if !tc.reported {
					suite.Failf("reported", "an erased tenant's event was routed to failed-events: %+v", item.event)
				}
			case <-time.After(2 * time.Second):
				if tc.reported {
					suite.Fail("an event that failed at the cap for an ordinary reason was not reported")
				}
			}
			if !tc.reported {
				suite.Equal(int32(1), ack.n.Load(), "an erased tenant's event must be settled on the delivery it arrived on")
				suite.Equal(1.0, loopResults(suite.T(), reg, "resolve", core.ResultErased), "resolve_messages_total{result=erased}")
			}
		})
	}
}

// An event of a tenant that has been DELETED is settled and reported nowhere whatever its
// resolution failed on — here a device the deletion has already removed, which never reaches a
// write the erasure fence could refuse. It is settled on its FIRST delivery, not retried to the
// cap and then recorded on failed-events. The counterweight is a live tenant failing the same
// way: retried, and reported at the cap, so the deleted rows cannot pass on a path that never
// reports. The gate is reached the way the service reaches it: through the processor's
// dead-letter producer, which the resolver pool consults when a resolution fails.
func (suite *InboundEventsProcessorTestSuite) TestADeletedTenantsUnresolvableEventIsSettledNotReported() {
	for _, tc := range []struct {
		name      string
		deleted   bool
		delivered int
		acked     int32
		reported  bool
	}{
		{"deleted tenant, first delivery", true, 1, 1, false},
		{"deleted tenant, final delivery", true, messaging.MaxDeliver, 1, false},
		{"control: live tenant, first delivery", false, 1, 0, false},
		{"control: live tenant, final delivery", false, messaging.MaxDeliver, 0, true},
	} {
		suite.Run(tc.name, func() {
			suite.SetupTest()
			reg := suite.withRegistry()
			producer := deadletter.NewProducer(&core.Microservice{FunctionalArea: "device-management"})
			deleted := tc.deleted
			if !producer.UseTenantGate(func(string) bool { return deleted }) {
				suite.FailNow("the producer's tenant gate was already built")
			}
			suite.IP.deadLetters = producer

			bytes, err := esproto.MarshalUnresolvedEvent(buildNewAssignmentEvent())
			suite.Require().NoError(err)
			ack := &atomicAck{}
			msg := messaging.NewConsumedMessage(testTenantSubject, bytes, tc.delivered, nil, ack)
			suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
			// The device is gone: resolution fails before anything is written.
			suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{}, nil)

			readAndHandleOne(suite.IP, context.Background())

			select {
			case item := <-suite.IP.failed:
				if !tc.reported {
					suite.Failf("reported", "an event was routed to failed-events: %+v", item.event)
				}
			case <-time.After(2 * time.Second):
				if tc.reported {
					suite.Fail("a live tenant's event that failed at the cap was not reported")
				}
			}
			if !tc.reported {
				suite.Equal(tc.acked, ack.n.Load(), "acknowledgements")
				suite.Equal(float64(tc.acked), loopResults(suite.T(), reg, "resolve", core.ResultErased),
					"resolve_messages_total{result=erased}")
			}
		})
	}
}
