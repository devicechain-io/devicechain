// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/mock"
)

// Mock for a message reader.
type MockMessageReader struct {
	mock.Mock
}

func (reader *MockMessageReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	args := reader.Called()
	return args.Get(0).(messaging.Message), args.Error(1)
}

func (reader *MockMessageReader) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Msg("read operation failed")
	}
}

// Mock for a message writer.
type MockMessageWriter struct {
	mock.Mock
	// Devices are the targets passed to WriteToDevice, in call order.
	Devices []string
}

func (writer *MockMessageWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	args := writer.Called()
	return args.Error(0)
}

// WriteToDevice records a per-device publish.
//
// NOTE: testify derives the expectation name from the CALLER, so this resolves
// .On("WriteToDevice") — NOT .On("WriteMessages"). A test whose production path
// moves onto this method must set its own expectation; it will not inherit the
// WriteMessages one.
//
// Devices records the target of each call so a test can assert a message went to
// ONE device. Asserting only that something was published cannot distinguish
// per-device addressing from a tenant-wide broadcast, which is the property the
// per-device subject exists to provide.
func (writer *MockMessageWriter) WriteToDevice(ctx context.Context, deviceToken string, msgs ...messaging.Message) error {
	writer.Devices = append(writer.Devices, deviceToken)
	args := writer.Called()
	return args.Error(0)
}

func (writer *MockMessageWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Msg("write operation failed")
	}
}
