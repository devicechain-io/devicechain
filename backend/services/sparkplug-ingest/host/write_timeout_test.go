// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/stretchr/testify/assert"
)

// paho installs no write deadline at zero, so a write to a dead peer blocks for good and the
// ping timeout is never checked. The built options must carry a bound.
func TestSessionOptionsBoundWritesAndStateKeepAlive(t *testing.T) {
	c := newTestClient(config.SparkplugSource{Tenant: "t", HostId: "h"}, Broker{}, nil, nil, nil, Metrics{})
	opts := c.sessionOptions(context.Background(), 1, make(chan struct{}, 1), new(atomic.Bool))
	assert.Equal(t, mqttWriteTimeout, opts.WriteTimeout)
	assert.Greater(t, int64(opts.WriteTimeout), int64(0))
	assert.Equal(t, mqttKeepAlive, time.Duration(opts.KeepAlive)*time.Second)
}
