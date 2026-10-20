// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package entityevent

import (
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"google.golang.org/protobuf/proto"
)

// Marshal an entity-deletion event to protobuf bytes (ADR-044).
func MarshalEntityDeletedEvent(event *EntityDeletedEvent) ([]byte, error) {
	pbevent := &PEntityDeletedEvent{
		EntityType:  string(event.EntityType),
		EntityId:    uint64(event.EntityId),
		EntityToken: event.EntityToken,
		DeletedTime: event.DeletedTime.Format(time.RFC3339Nano),
	}
	return proto.Marshal(pbevent)
}

// Unmarshal an encoded entity-deletion event.
func UnmarshalEntityDeletedEvent(encoded []byte) (*EntityDeletedEvent, error) {
	pbevent := &PEntityDeletedEvent{}
	if err := proto.Unmarshal(encoded, pbevent); err != nil {
		return nil, err
	}
	deleted, err := time.Parse(time.RFC3339Nano, pbevent.DeletedTime)
	if err != nil {
		return nil, err
	}
	return &EntityDeletedEvent{
		EntityType:  entity.Type(pbevent.EntityType),
		EntityId:    uint(pbevent.EntityId),
		EntityToken: pbevent.EntityToken,
		DeletedTime: deleted,
	}, nil
}
