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
