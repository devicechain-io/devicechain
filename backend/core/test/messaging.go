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
}

func (writer *MockMessageWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	args := writer.Called()
	return args.Error(0)
}

func (writer *MockMessageWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Msg("write operation failed")
	}
}
