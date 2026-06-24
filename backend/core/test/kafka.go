// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/mock"

	kafka "github.com/segmentio/kafka-go"
)

// Mock for Kafka reader.
type MockKafkaReader struct {
	mock.Mock
}

func (reader *MockKafkaReader) ReadMessage(ctx context.Context) (kafka.Message, error) {
	args := reader.Called()
	return args.Get(0).(kafka.Message), args.Error(1)
}

func (reader *MockKafkaReader) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Msg("read operation failed")
	}
}

// Mock for Kafka writer
type MockKafkaWriter struct {
	mock.Mock
}

func (writer *MockKafkaWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	args := writer.Called()
	return args.Error(0)
}

func (reader *MockKafkaWriter) HandleResponse(err error) {
	if err != nil {
		log.Error().Err(err).Msg("write operation failed")
	}
}
