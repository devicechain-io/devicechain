// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// committedSeqLoader is the part of the snapshot store a floor reader's start position needs.
type committedSeqLoader interface {
	LoadCommittedSeq(ctx context.Context, partitionId string) (int64, bool, error)
}

// CommittedFloorStart is the start position of the resolved-events durable: the first
// sequence after the committed snapshot, or the first sequence of the stream when there is
// none. messaging.ReaderWithAckFloor calls it only when the durable has to be created (the
// first run of this build, and a durable that was deleted), because an existing durable is
// bound where its own ack floor stands.
//
// It reads the committed ROW, not the engine, because the reader is bound before restore
// builds the engine. It reads only the sequence column, not the snapshot payload.
func CommittedFloorStart(store committedSeqLoader, partitionId string) func(context.Context) (uint64, error) {
	return func(ctx context.Context) (uint64, error) {
		seq, ok, err := store.LoadCommittedSeq(ctx, partitionId)
		if err != nil {
			return 0, fmt.Errorf("event-processing: reading the committed DETECT snapshot sequence for partition %q: %w", partitionId, err)
		}
		if !ok || seq < 0 {
			return 1, nil
		}
		return uint64(seq) + 1, nil
	}
}

// ResolvedEventsReaderOptions are the options the DETECT tap's resolved-events reader is built
// with, beyond its term gate. main builds the reader with them and the broker tests build
// theirs with them too, so that what the tests run is what production runs.
func ResolvedEventsReaderOptions(store committedSeqLoader, partitionId string) []messaging.ReaderOption {
	return []messaging.ReaderOption{messaging.ReaderWithAckFloor(CommittedFloorStart(store, partitionId))}
}
