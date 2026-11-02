// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"

	"github.com/devicechain-io/dc-event-management/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"google.golang.org/protobuf/proto"
)

// The areas that write the failed-events stream (core/streams declares them). A record from any
// other area is indexed all the same, under the source "other" on the metric.
const (
	areaDeviceManagement = "device-management"
	areaEventManagement  = "event-management"
)

// FailedRecord is what the index keeps of one failed-events record: the fields of its envelope,
// plus the device token and event type read out of the archived event when there is one.
type FailedRecord struct {
	Reason      int32
	SourceArea  string
	DeviceToken string
	EventType   *int64
	Error       string
}

// ParseFailedRecord reads one failed-events record.
//
// The envelope is the platform's own (PFailedEvent), so one that will not decode is not a
// record: the error is returned and the caller drops it. The ARCHIVED EVENT inside it is a
// different matter and is never fatal. It is read only for the device the sender claimed and
// the event type, and a payload that will not decode simply leaves them empty -- the record is
// still a record of a refusal, and refusing to index it because it was also malformed would
// lose exactly the cases an operator is looking for.
//
// What is read depends on who wrote it, because the two writers archive different things:
// device-management archives the UNRESOLVED event, as the sender framed it (the device token is
// the sender's claim); event-management archives the RESOLVED event it could not store (the
// token is the device's own). A reason of Invalid archives either nothing or the raw bytes that
// would not parse, which are not read at all.
func ParseFailedRecord(value []byte) (*FailedRecord, error) {
	pf := &resolvedevent.PFailedEvent{}
	if err := proto.Unmarshal(value, pf); err != nil {
		return nil, fmt.Errorf("failed-event record does not decode: %w", err)
	}
	rec := &FailedRecord{
		Reason:     int32(pf.Reason),
		SourceArea: model.SanitizeFailedEventText(pf.Service, 64),
		Error:      tenantVisibleError(pf),
	}
	if len(pf.Payload) == 0 || pf.Reason == resolvedevent.FailureReason_Invalid {
		return rec, nil
	}
	switch pf.Service {
	case areaDeviceManagement:
		var ue esproto.PUnresolvedEvent
		if proto.Unmarshal(pf.Payload, &ue) == nil {
			rec.DeviceToken = ue.Device
			if rec.DeviceToken == "" {
				rec.DeviceToken = ue.IntegrationIdentifier
			}
			et := ue.EventType
			rec.EventType = &et
		}
	case areaEventManagement:
		var re resolvedevent.PResolvedEvent
		if proto.Unmarshal(pf.Payload, &re) == nil {
			rec.DeviceToken = re.SourceDeviceToken
			et := re.EventType
			rec.EventType = &et
		}
	}
	rec.DeviceToken = model.ClaimedDeviceToken(rec.DeviceToken)
	return rec, nil
}

// tenantVisibleError is the error text a tenant's administrators are shown for a record.
//
// Every writer's text is the platform's own wording about the tenant's own traffic, with two
// exceptions this decides. A decode error can quote the bytes it could not parse (the Invalid
// writers bound it, but the bound is theirs), so it is sanitized and bounded here as well. And
// ApiCallFailed carries whatever the failing internal call said -- a service name, an address,
// a driver's message -- which describes the platform's wiring and not the tenant's traffic, so
// the tenant is told the category and the operator keeps the detail in the logs.
func tenantVisibleError(pf *resolvedevent.PFailedEvent) string {
	if pf.Reason == resolvedevent.FailureReason_ApiCallFailed {
		return "a platform service could not be reached while the event was being processed; the platform's logs have the detail"
	}
	return model.SanitizeFailedEventText(pf.Error, model.MaxFailedEventError)
}
