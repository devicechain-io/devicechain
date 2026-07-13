// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidConnectorType covers the registered vocabulary boundary.
func TestValidConnectorType(t *testing.T) {
	for _, ok := range []string{"mqtt", "kafka", "aws_sns", "aws_sqs", "gcp_pubsub"} {
		assert.True(t, ValidConnectorType(ok), "%q should be valid", ok)
	}
	for _, bad := range []string{"", "MQTT", "http", "amqp", "carrier_pigeon"} {
		assert.False(t, ValidConnectorType(bad), "%q should be invalid", bad)
	}
}

// TestConnectorTypesSorted returns the full vocabulary, sorted.
func TestConnectorTypesSorted(t *testing.T) {
	assert.Equal(t, []string{"aws_sns", "aws_sqs", "gcp_pubsub", "kafka", "mqtt"}, ConnectorTypes())
}
