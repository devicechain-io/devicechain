// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"fmt"
	"io"
	"testing"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

// isConsumerGone must fire for every "the durable is no longer usable" error so the
// reader self-heals (re-binds) rather than surfacing the error and hot-spinning, and
// must NOT fire for a transient timeout or a shutdown-time close (those have their
// own handling).
func TestIsConsumerGone(t *testing.T) {
	gone := []error{
		nats.ErrConsumerDeleted,
		nats.ErrConsumerNotFound,
		nats.ErrConsumerNotActive,
		nats.ErrNoResponders,
		// Still detected when wrapped, since the reader wraps/propagates Fetch errors.
		fmt.Errorf("fetch failed: %w", nats.ErrConsumerDeleted),
	}
	for _, err := range gone {
		assert.True(t, isConsumerGone(err), "expected consumer-gone for %v", err)
	}

	notGone := []error{
		nats.ErrTimeout,
		nats.ErrConnectionClosed,
		nats.ErrSubscriptionClosed,
		nats.ErrConnectionDraining,
		io.EOF,
		errors.New("some other error"),
		nil,
	}
	for _, err := range notGone {
		assert.False(t, isConsumerGone(err), "expected NOT consumer-gone for %v", err)
	}
}
