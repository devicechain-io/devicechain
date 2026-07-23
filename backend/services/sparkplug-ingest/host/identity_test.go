// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSparkplugExternalIdShape pins the ADR-049 external id: a node message maps to
// "{group}/{node}", a device message to "{group}/{node}/{device}". The token DERIVATION
// from this id is protocol-neutral and its tests live with the adapter
// (adapter/token_test.go); only this Sparkplug-topic → external-id mapping is tested here.
func TestSparkplugExternalIdShape(t *testing.T) {
	assert.Equal(t, "g/n", SparkplugExternalId(nTop(NDATA)))
	assert.Equal(t, "g/n/sensor-7", SparkplugExternalId(dTop(DDATA, "sensor-7")))
}
