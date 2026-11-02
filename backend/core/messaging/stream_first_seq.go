// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"

	nats "github.com/nats-io/nats.go"
)

// StreamFirstSequence returns the sequence of the oldest message the stream still holds. Every
// message below it has been discarded by the stream's limits, so a record located by a sequence
// beneath it is gone for good.
//
// An empty stream reports its next sequence (last + 1): nothing at or beyond a sequence it has
// already passed is held. The error is the broker's answer when it could not give one (the
// stream is missing, or the broker is unreachable); a caller must report that as UNKNOWN and
// never as "held" or "gone", because either would be a claim about a record it did not look at.
func (nmgr *NatsManager) StreamFirstSequence(ctx context.Context, suffix string) (uint64, error) {
	if nmgr == nil || nmgr.js == nil {
		return 0, errors.New("the broker connection is not available")
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	info, err := nmgr.js.StreamInfo(name, nats.Context(ctx))
	if err != nil {
		return 0, fmt.Errorf("reading stream %q: %w", name, err)
	}
	if info.State.Msgs == 0 {
		return info.State.LastSeq + 1, nil
	}
	return info.State.FirstSeq, nil
}
