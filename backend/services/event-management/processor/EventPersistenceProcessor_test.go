// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	test "github.com/devicechain-io/dc-microservice/test"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// Topic carrying a parseable tenant ({instance}.{tenant}.{suffix}) so the
// persistence worker can derive a per-message tenant context (fail-closed).
const testTenantSubject = "instance1.tenant1.resolved-events"

type EventPersistenceProcessorTestSuite struct {
	suite.Suite
	EP      *EventPersistenceProcessor
	Inbound *test.MockMessageReader
	Failed  *test.MockMessageWriter
	API     *emtest.MockApi
}

// Perform common setup tasks.
func (suite *EventPersistenceProcessorTestSuite) SetupTest() {
	// The persist loop's ProcessorMetrics (E13) registers collectors on the
	// global default registry at construction. SetupTest builds a fresh
	// processor per test method, so reset the default registry first to avoid a
	// duplicate-registration panic across tests.
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry

	suite.Inbound = new(test.MockMessageReader)
	suite.Failed = new(test.MockMessageWriter)
	suite.API = new(emtest.MockApi)
	suite.EP = NewEventPersistenceProcessor(
		dmtest.DeviceManagementMicroservice,
		suite.Inbound,
		suite.Failed,
		core.NewNoOpLifecycleCallbacks(),
		suite.API)
	ctx := context.Background()
	suite.EP.Initialize(ctx)
}

// Test processing loop termination on EOF.
func (suite *EventPersistenceProcessorTestSuite) TestLifecycle() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{}, io.EOF)
	err := suite.EP.Start(context.Background())
	assert.Nil(suite.T(), err)
	err = suite.EP.Stop(context.Background())
	assert.Nil(suite.T(), err)
	err = suite.EP.Terminate(context.Background())
	assert.Nil(suite.T(), err)
}

// Test processing loop termination on EOF.
func (suite *EventPersistenceProcessorTestSuite) TestProcessingLoopEof() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{}, io.EOF)

	eof := suite.EP.ProcessMessage(context.Background())

	assert.Equal(suite.T(), eof, true)
}

// Test processing loop without EOF.
func (suite *EventPersistenceProcessorTestSuite) TestProcessingLoopNonEof() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{}, nil)

	eof := suite.EP.ProcessMessage(context.Background())

	assert.Equal(suite.T(), eof, false)
}

// Build resolved event with the given payload.
func buildResolvedEvent(etype esmodel.EventType, payload interface{}) *dmodel.ResolvedEvent {
	altid := "alternateId"
	return &dmodel.ResolvedEvent{
		Source:            "mysource",
		AltId:             &altid,
		SourceDeviceToken: "device-1",
		Anchors: []dmodel.ResolvedAnchor{
			{AnchorType: "asset", AnchorToken: "asset-1", RelationshipId: 1},
		},
		EventType: etype,
		Payload:   payload,
	}
}

// Build a locations event.
func buildLocationsEvent() *dmodel.ResolvedEvent {
	lat := "33.7490"
	lon := "-84.3880"
	ele := "738"
	entry := dmodel.ResolvedLocationEntry{
		Latitude:  &lat,
		Longitude: &lon,
		Elevation: &ele,
	}
	entries := make([]dmodel.ResolvedLocationEntry, 0)
	entries = append(entries, entry)
	loc := &dmodel.ResolvedLocationsPayload{
		Entries: entries,
	}
	return buildResolvedEvent(esmodel.Location, loc)
}

// Build a measurements event.
func buildMeasurementsEvent() *dmodel.ResolvedEvent {
	mxs := make([]dmodel.ResolvedMeasurementEntry, 0)
	mx1 := dmodel.ResolvedMeasurementEntry{
		Name:       "temp:inDegreesCelcius",
		Value:      "101.5",
		Classifier: nil,
	}
	mx2 := dmodel.ResolvedMeasurementEntry{
		Name:       "speed:inMilesPerHour",
		Value:      "77.5",
		Classifier: nil,
	}
	mxs = append(mxs, mx1)
	mxs = append(mxs, mx2)

	entry := dmodel.ResolvedMeasurementsEntry{
		Entries: mxs,
	}
	entries := make([]dmodel.ResolvedMeasurementsEntry, 0)
	entries = append(entries, entry)
	loc := &dmodel.ResolvedMeasurementsPayload{
		Entries: entries,
	}
	return buildResolvedEvent(esmodel.Measurement, loc)
}

// Build an alerts event.
func buildAlertsEvent() *dmodel.ResolvedEvent {
	entry := dmodel.ResolvedAlertEntry{
		Type:    "engine.overheat",
		Level:   3,
		Message: "Engine temperature exceeds threshold",
		Source:  "ecu",
	}
	entries := make([]dmodel.ResolvedAlertEntry, 0)
	entries = append(entries, entry)
	alert := &dmodel.ResolvedAlertsPayload{
		Entries: entries,
	}
	return buildResolvedEvent(esmodel.Alert, alert)
}

// Build a state change (presence) event. Unlike the others it carries NO AltId — a
// StateChange has no base-event dedup key (ADR-067); redelivery dedup is the history
// table's idempotency index.
func buildStateChangeEvent() *dmodel.ResolvedEvent {
	sc := &dmodel.ResolvedStateChangePayload{
		State:     "DISCONNECTED",
		Reason:    "lifetime-lapse",
		SessionId: 42,
	}
	ev := buildResolvedEvent(esmodel.StateChange, sc)
	ev.AltId = nil
	return ev
}

// Test failed event flow for a given message.
func (suite *EventPersistenceProcessorTestSuite) FailedEventFlowFor(msg messaging.Message) {
	// Emulate read/write.
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
	suite.Failed.Mock.On("WriteMessages", mock.Anything, mock.Anything).Return(nil)

	// Send message and wait for event to be processed by resolver.
	ctx := context.Background()
	suite.EP.ProcessMessage(ctx)
	suite.EP.ProcessFailedEvent(ctx)

	// Verify a message was written to failed messages writer.
	suite.Failed.AssertCalled(suite.T(), "WriteMessages", mock.Anything, mock.Anything)
}

// Test invalid event.
func (suite *EventPersistenceProcessorTestSuite) TestInvalidEvent() {
	// Assuming invalid binary message format..
	key := []byte("test")
	value := []byte("badvalue")
	badmsg := messaging.Message{Subject: testTenantSubject, Key: key, Value: value}

	// Test event flow.
	suite.API.Mock.On("CreateLocationEvents", mock.Anything, mock.Anything).Return([]*model.LocationEvent{}, nil)
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
	suite.FailedEventFlowFor(badmsg)
}

// Test valid event flow for a given message.
func (suite *EventPersistenceProcessorTestSuite) SuccessEventFlowFor(msg messaging.Message) {
	// Emulate read.
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)

	// Send message. The worker pool started in Initialize persists the event
	// asynchronously, so poll until the API records the corresponding create
	// call (the persistence side effect) rather than synchronizing on a now
	// removed persisted channel.
	ctx := context.Background()
	suite.EP.ProcessMessage(ctx)

	// Verify the event was persisted via the API.
	assert.Eventually(suite.T(), func() bool {
		return len(suite.API.Mock.Calls) > 0
	}, 2*time.Second, 10*time.Millisecond, "expected event to be persisted via the API")
}

// Test locations event with one entry.
func (suite *EventPersistenceProcessorTestSuite) TestSingleLocationEvent() {
	// Encode payload as bytes.
	loc := buildLocationsEvent()
	bytes, err := dmproto.MarshalResolvedEvent(loc)
	assert.Nil(suite.T(), err)

	// Build message.
	key := []byte(loc.Source)
	msg := messaging.Message{Subject: testTenantSubject, Key: key, Value: bytes}

	// Test event flow.
	suite.API.Mock.On("CreateLocationEvents", mock.Anything, mock.Anything).Return([]*model.LocationEvent{{}}, nil)
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
	suite.SuccessEventFlowFor(msg)
}

// Test measurements event with one entry.
func (suite *EventPersistenceProcessorTestSuite) TestSingleMeasurementEvent() {
	// Encode payload as bytes.
	loc := buildMeasurementsEvent()
	bytes, err := dmproto.MarshalResolvedEvent(loc)
	assert.Nil(suite.T(), err)

	// Build message.
	key := []byte(loc.Source)
	msg := messaging.Message{Subject: testTenantSubject, Key: key, Value: bytes}

	// Test event flow.
	suite.API.Mock.On("CreateMeasurementEvents", mock.Anything, mock.Anything).Return([]*model.MeasurementEvent{{}, {}}, nil)
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
	suite.SuccessEventFlowFor(msg)
}

// Test alerts event with one entry.
func (suite *EventPersistenceProcessorTestSuite) TestSingleAlertEvent() {
	// Encode payload as bytes.
	alert := buildAlertsEvent()
	bytes, err := dmproto.MarshalResolvedEvent(alert)
	assert.Nil(suite.T(), err)

	// Build message.
	key := []byte(alert.Source)
	msg := messaging.Message{Subject: testTenantSubject, Key: key, Value: bytes}

	// Test event flow.
	suite.API.Mock.On("CreateAlertEvents", mock.Anything, mock.Anything).Return([]*model.AlertEvent{{}}, nil)
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
	suite.SuccessEventFlowFor(msg)
}

// Test a state change (presence) event persists to history (ADR-067 S3): it ROUTES to
// CreateStateChangeEvents (no longer the ack-skip no-op) and falls through to anchor
// persistence like every other event type — the two behavioral changes of the S3a
// dispatch. Driven synchronously through the worker's PersistEvent (no async pool) so
// the mock-call assertions are deterministic and race-free.
func (suite *EventPersistenceProcessorTestSuite) TestStateChangeEventPersists() {
	worker := &EventPersistenceWorker{Api: suite.API}
	suite.API.Mock.On("CreateStateChangeEvents", mock.Anything, mock.Anything).Return([]*model.StateChangeEvent{{}}, int64(1), nil)
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)

	results, err := worker.PersistEvent(context.Background(), *buildStateChangeEvent())
	assert.Nil(suite.T(), err)
	assert.NotNil(suite.T(), results)

	// Routing to history persistence AND the anchor fall-through are the S3a changes —
	// the ack-skip no-op did NEITHER.
	suite.API.AssertCalled(suite.T(), "CreateStateChangeEvents", mock.Anything, mock.Anything)
	suite.API.AssertCalled(suite.T(), "CreateEventAnchors", mock.Anything, mock.Anything)
}

// A redelivered StateChange (RowsAffected==0 from the idempotency index) must NOT
// re-run anchor persistence — event_anchors has no unique index, so a plain re-insert
// would duplicate the anchor set (a StateChange never carries an AltId, so the base
// dedup does not engage). The FIRST delivery persists + anchors; the SECOND persists
// nothing and skips anchors.
func (suite *EventPersistenceProcessorTestSuite) TestStateChangeRedeliverySkipsAnchors() {
	worker := &EventPersistenceWorker{Api: suite.API}
	// First delivery inserts (RowsAffected 1); redelivery deduplicates (RowsAffected 0).
	suite.API.Mock.On("CreateStateChangeEvents", mock.Anything, mock.Anything).Return([]*model.StateChangeEvent{{}}, int64(1), nil).Once()
	suite.API.Mock.On("CreateStateChangeEvents", mock.Anything, mock.Anything).Return([]*model.StateChangeEvent{}, int64(0), nil).Once()
	suite.API.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)

	sc := *buildStateChangeEvent()
	if _, err := worker.PersistEvent(context.Background(), sc); err != nil {
		suite.T().Fatalf("first delivery: %v", err)
	}
	if _, err := worker.PersistEvent(context.Background(), sc); err != nil {
		suite.T().Fatalf("redelivery: %v", err)
	}

	// Anchors persisted exactly ONCE across the two deliveries.
	suite.API.AssertNumberOfCalls(suite.T(), "CreateEventAnchors", 1)
}

// Run all tests.
func TestEventPersistenceProcessorTestSuite(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	suite.Run(t, new(EventPersistenceProcessorTestSuite))
}
