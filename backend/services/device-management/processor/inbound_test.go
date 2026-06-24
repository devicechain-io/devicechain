/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package processor

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	test "github.com/devicechain-io/dc-microservice/test"
	"github.com/rs/zerolog"
	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type InboundEventsProcessorTestSuite struct {
	suite.Suite
	IP       *InboundEventsProcessor
	Inbound  *test.MockKafkaReader
	Resolved *test.MockKafkaWriter
	Failed   *test.MockKafkaWriter
	API      *dmtest.MockApi
}

// Perform common setup tasks.
func (suite *InboundEventsProcessorTestSuite) SetupTest() {
	suite.Inbound = new(test.MockKafkaReader)
	suite.Resolved = new(test.MockKafkaWriter)
	suite.Failed = new(test.MockKafkaWriter)
	suite.API = new(dmtest.MockApi)
	suite.IP = NewInboundEventsProcessor(
		dmtest.DeviceManagementMicroservice,
		suite.Inbound,
		suite.Resolved,
		suite.Failed,
		core.NewNoOpLifecycleCallbacks(),
		suite.API)
	ctx := context.Background()
	suite.IP.Initialize(ctx)
}

// Test processing loop termination on EOF.
func (suite *InboundEventsProcessorTestSuite) TestLifecycle() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(kafka.Message{}, io.EOF)
	err := suite.IP.Start(context.Background())
	assert.Nil(suite.T(), err)
	err = suite.IP.Stop(context.Background())
	assert.Nil(suite.T(), err)
	err = suite.IP.Terminate(context.Background())
	assert.Nil(suite.T(), err)
}

// Test processing loop termination on EOF.
func (suite *InboundEventsProcessorTestSuite) TestProcessingLoopEof() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(kafka.Message{}, io.EOF)

	eof := suite.IP.ProcessMessage(context.Background())

	assert.Equal(suite.T(), eof, true)
}

// Test processing loop without EOF.
func (suite *InboundEventsProcessorTestSuite) TestProcessingLoopNonEof() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(kafka.Message{}, nil)

	eof := suite.IP.ProcessMessage(context.Background())

	assert.Equal(suite.T(), eof, false)
}

// Test event with invalid protobuf content.
func (suite *InboundEventsProcessorTestSuite) TestInvalidEvent() {
	// Assuming invalid binary message format..
	key := []byte("test")
	value := []byte("badvalue")
	badmsg := kafka.Message{Key: key, Value: value}

	// Emulate kafka read/write.
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(badmsg, nil)
	suite.Failed.Mock.On("WriteMessages", mock.Anything, mock.Anything).Return(nil)

	// Send message and wait for event to be processed by resolver.
	ctx := context.Background()
	suite.IP.ProcessMessage(ctx)
	suite.IP.ProcessFailedEvent(ctx)

	// Verify a message was written to failed messages writer.
	suite.Failed.AssertCalled(suite.T(), "WriteMessages", mock.Anything, mock.Anything)
}

// Build a new assignment event.
func buildNewAssignmentEvent() *model.UnresolvedEvent {
	dreltype := "controls"
	dgroup := "primary"
	asset := "car123"
	agroup := "cars"
	assn := &model.UnresolvedNewRelationshipPayload{
		DeviceRelationshipType: dreltype,
		TargetDeviceGroup:      &dgroup,
		TargetAsset:            &asset,
		TargetAssetGroup:       &agroup,
	}
	altid := "alternateId"
	event := &model.UnresolvedEvent{
		Source:    "mysource",
		AltId:     &altid,
		Device:    "TEST-123",
		EventType: model.NewRelationship,
		Payload:   assn,
	}
	return event
}

// Build a locations event.
func buildLocationsEvent() *model.UnresolvedEvent {
	lat := "33.7490"
	lon := "-84.3880"
	ele := "738"
	entry := model.UnresolvedLocationEntry{
		Latitude:  &lat,
		Longitude: &lon,
		Elevation: &ele,
	}
	entries := make([]model.UnresolvedLocationEntry, 0)
	entries = append(entries, entry)
	loc := &model.UnresolvedLocationsPayload{
		Entries: entries,
	}
	altid := "alternateId"
	event := &model.UnresolvedEvent{
		Source:    "mysource",
		AltId:     &altid,
		Device:    "TEST-123",
		EventType: model.Location,
		Payload:   loc,
	}
	return event
}

// Build a measurements event.
func buildMeasurementsEvent() *model.UnresolvedEvent {
	mxs := make(map[string]string, 0)
	mxs["temp.inDegreesCelcius"] = "101.5"
	mxs["speed.inMilesPerHour"] = "77.5"

	entry := model.UnresolvedMeasurementsEntry{
		Measurements: mxs,
	}
	entries := make([]model.UnresolvedMeasurementsEntry, 0)
	entries = append(entries, entry)
	mxpayload := &model.UnresolvedMeasurementsPayload{
		Entries: entries,
	}
	altid := "alternateId"
	event := &model.UnresolvedEvent{
		Source:    "mysource",
		AltId:     &altid,
		Device:    "TEST-123",
		EventType: model.Measurement,
		Payload:   mxpayload,
	}
	return event
}

// Build an alerts event.
func buildAlertsEvent() *model.UnresolvedEvent {
	entry := model.UnresolvedAlertEntry{
		Type:    "engine.overheat",
		Level:   10,
		Message: "engine is overheating",
		Source:  "coolant-monitor",
	}
	entries := make([]model.UnresolvedAlertEntry, 0)
	entries = append(entries, entry)
	mxpayload := &model.UnresolvedAlertsPayload{
		Entries: entries,
	}
	altid := "alternateId"
	event := &model.UnresolvedEvent{
		Source:    "mysource",
		AltId:     &altid,
		Device:    "TEST-123",
		EventType: model.Alert,
		Payload:   mxpayload,
	}
	return event
}

// Build a location event.
func buildDevice() *dmodel.Device {
	name := "Test 123"
	desc := "Device test 123"
	dtype := uint(123)
	device := &dmodel.Device{
		Model: gorm.Model{
			ID:        1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		TokenReference: rdb.TokenReference{
			Token: "TEST-123",
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(&name),
			Description: rdb.NullStrOf(&desc),
		},
		DeviceTypeId: dtype,
	}
	return device
}

// Test valid location event.
func (suite *InboundEventsProcessorTestSuite) TestUnresolvableLocationsEvent() {
	loc := buildLocationsEvent()
	bytes, err := esproto.MarshalUnresolvedEvent(loc)
	assert.Nil(suite.T(), err)

	// Assuming invalid binary message format..
	key := []byte(loc.Device)
	msg := kafka.Message{Key: key, Value: bytes}

	// Emulate kafka read/write.
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
	suite.Failed.Mock.On("WriteMessages", mock.Anything, mock.Anything).Return(nil)
	suite.API.Mock.On("DeviceByToken", mock.Anything, mock.Anything).Return(&dmodel.Device{}, errors.New("not found"))

	// Send message and wait for event to be processed by resolver.
	ctx := context.Background()
	suite.IP.ProcessMessage(ctx)
	suite.IP.ProcessFailedEvent(ctx)

	// Verify a message was written to failed messages writer.
	suite.Failed.AssertCalled(suite.T(), "WriteMessages", mock.Anything, mock.Anything)
}

// Test valid event flow for a given message.
func (suite *InboundEventsProcessorTestSuite) SuccessEventFlowFor(msg kafka.Message) {
	// Emulate kafka read/write.
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
	suite.Resolved.Mock.On("WriteMessages", mock.Anything, mock.Anything).Return(nil)
	suite.API.Mock.On("DeviceByToken", mock.Anything, mock.Anything).Return(buildDevice(), nil)

	// Send message and wait for event to be processed by resolver.
	ctx := context.Background()
	suite.IP.ProcessMessage(ctx)
	suite.IP.ProcessResolvedEvent(ctx)

	// Verify a message was written to resolved messages writer.
	suite.Resolved.AssertCalled(suite.T(), "WriteMessages", mock.Anything, mock.Anything)
}

// Test valid new assignment event.
func (suite *InboundEventsProcessorTestSuite) TestValidNewAssignmentEvent() {
	nassn := buildNewAssignmentEvent()
	bytes, err := esproto.MarshalUnresolvedEvent(nassn)
	assert.Nil(suite.T(), err)

	key := []byte(nassn.Device)
	msg := kafka.Message{Key: key, Value: bytes}
	suite.SuccessEventFlowFor(msg)
}

// Test valid location event.
func (suite *InboundEventsProcessorTestSuite) TestValidLocationsEvent() {
	loc := buildLocationsEvent()
	bytes, err := esproto.MarshalUnresolvedEvent(loc)
	assert.Nil(suite.T(), err)

	key := []byte(loc.Device)
	msg := kafka.Message{Key: key, Value: bytes}
	suite.SuccessEventFlowFor(msg)
}

// Test valid measurements event.
func (suite *InboundEventsProcessorTestSuite) TestValidMeasurementsEvent() {
	mxs := buildMeasurementsEvent()
	bytes, err := esproto.MarshalUnresolvedEvent(mxs)
	assert.Nil(suite.T(), err)

	key := []byte(mxs.Device)
	msg := kafka.Message{Key: key, Value: bytes}
	suite.SuccessEventFlowFor(msg)
}

// Test valid alerts event.
func (suite *InboundEventsProcessorTestSuite) TestValidAlertsEvent() {
	alerts := buildAlertsEvent()
	bytes, err := esproto.MarshalUnresolvedEvent(alerts)
	assert.Nil(suite.T(), err)

	key := []byte(alerts.Device)
	msg := kafka.Message{Key: key, Value: bytes}
	suite.SuccessEventFlowFor(msg)
}

// Run all tests.
func TestInboundEventsProcessorTestSuite(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	suite.Run(t, new(InboundEventsProcessorTestSuite))
}
