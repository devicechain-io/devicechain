// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

type legacyDeleteRecorder struct {
	nats.JetStreamContext
	deleted []string
}

func (j *legacyDeleteRecorder) DeleteConsumer(stream, consumer string, _ ...nats.JSOpt) error {
	j.deleted = append(j.deleted, stream+"."+consumer)
	return nats.ErrConsumerNotFound
}

func TestLegacyAckFloorCleanupOnlyTargetsTheOriginalStream(t *testing.T) {
	zero, one, two := 0, 1, 2
	for _, tc := range []struct {
		name     string
		group    *int
		ackFloor bool
		deletes  bool
	}{
		{name: "unsharded upgrade", ackFloor: true, deletes: true},
		{name: "group zero upgrade", group: &zero, ackFloor: true, deletes: true},
		{name: "group one", group: &one, ackFloor: true},
		{name: "group two", group: &two, ackFloor: true},
		{name: "explicit ack reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const instance = "legacycleanup"
			js := &legacyDeleteRecorder{}
			ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-processing"}
			physical := streams.ResolvedEvents
			if tc.group != nil {
				physical = GroupSuffix(physical, *tc.group)
			}
			r := &natsReader{}
			r.nmgr = &NatsManager{Microservice: ms, js: js}
			r.suffix, r.shardGroup = streams.ResolvedEvents, tc.group
			r.stream = StreamName(instance, physical)
			r.durable = AckFloorDurableName(instance, ms.FunctionalArea, physical)
			if tc.ackFloor {
				r.ackFloorStart = func(context.Context) (uint64, error) { return 1, nil }
			}
			r.retireLegacyDurable()
			if tc.deletes {
				require.Equal(t, []string{StreamName(instance, streams.ResolvedEvents) + "." +
					DurableName(instance, ms.FunctionalArea, streams.ResolvedEvents)}, js.deleted)
			} else {
				require.Empty(t, js.deleted, "this reader replaced no legacy explicit-ack durable")
			}
		})
	}
}
