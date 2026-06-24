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

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
)

// Worker used to persist event entities.
type EventPersistenceWorker struct {
	WorkerId    int
	Api         model.EventManagementApi
	Unpersisted <-chan kafka.Message
	Invalid     func(error, kafka.Message)
	Persisted   func(interface{})
	Failed      func(uint, dmmodel.ResolvedEvent, error)
}

// Results of event persistence process.
type EventPersistenceResults struct {
	Events []interface{}
}

// Create a new event resolver.
func NewEventPersistenceWorker(workerId int, api model.EventManagementApi,
	unpersisted <-chan kafka.Message,
	invalid func(error, kafka.Message),
	persisted func(interface{}),
	failed func(uint, dmmodel.ResolvedEvent, error)) *EventPersistenceWorker {
	return &EventPersistenceWorker{
		WorkerId:    workerId,
		Api:         api,
		Unpersisted: unpersisted,
		Invalid:     invalid,
		Persisted:   persisted,
		Failed:      failed,
	}
}

// Parse a (possibly null) string into a float64.
func parseNullableFloat64(val *string) (*float64, error) {
	if val == nil {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// Persists a location event to the datastore.
func (ep *EventPersistenceWorker) PersistLocationEvents(ctx context.Context, event model.Event,
	payload dmmodel.ResolvedLocationsPayload) (*EventPersistenceResults, error) {
	events := make([]interface{}, 0)
	for _, location := range payload.Entries {
		lat, err := parseNullableFloat64(location.Latitude)
		if err != nil {
			return nil, err
		}
		lon, err := parseNullableFloat64(location.Longitude)
		if err != nil {
			return nil, err
		}
		ele, err := parseNullableFloat64(location.Elevation)
		if err != nil {
			return nil, err
		}
		lreq := &model.LocationEventCreateRequest{
			Event:     event,
			Latitude:  lat,
			Longitude: lon,
			Elevation: ele,
		}
		locevt, err := ep.Api.CreateLocationEvent(ctx, lreq)
		if err != nil {
			return nil, err
		}
		events = append(events, locevt)
	}
	results := &EventPersistenceResults{
		Events: events,
	}
	return results, nil
}

// Persists a resolved event to the datastore.
func (ep *EventPersistenceWorker) PersistEvent(ctx context.Context, event dmmodel.ResolvedEvent) (*EventPersistenceResults, error) {
	pevent := model.Event{
		DeviceId:           event.SourceDeviceId,
		OccurredTime:       event.OccurredTime,
		Source:             event.Source,
		AltId:              rdb.NullStrOf(event.AltId),
		RelDeviceId:        event.TargetDeviceId,
		RelDeviceGroupId:   event.TargetDeviceGroupId,
		RelAssetId:         event.TargetAssetId,
		RelAssetGroupId:    event.TargetAssetGroupId,
		RelCustomerId:      event.TargetCustomerId,
		RelCustomerGroupId: event.TargetCustomerGroupId,
		RelAreaId:          event.TargetAreaId,
		RelAreaGroupId:     event.TargetAreaGroupId,
		ProcessedTime:      event.ProcessedTime,
		EventType:          event.EventType,
	}
	switch event.EventType {
	case esmodel.Location:
		if payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload); ok {
			return ep.PersistLocationEvents(ctx, pevent, *payload)
		}
		return nil, fmt.Errorf("non-location payload in location event")
	}
	return nil, fmt.Errorf("unhandled event type in persistence: %s", event.EventType.String())
}

// Converts unresolved events into resolved events.
func (ep *EventPersistenceWorker) Process(ctx context.Context) {
	for {
		unpersisted, more := <-ep.Unpersisted
		if more {
			log.Debug().Msg(fmt.Sprintf("Event persistence handled by worker id %d", ep.WorkerId))

			// Attempt to unmarshal event.
			event, err := dmproto.UnmarshalResolvedEvent(unpersisted.Value)
			if err != nil {
				ep.Invalid(err, unpersisted)
				continue
			}

			if log.Debug().Enabled() {
				jevent, err := json.MarshalIndent(event, "", "  ")
				if err == nil {
					log.Debug().Msg(fmt.Sprintf("Received %s event:\n%s", event.EventType.String(), jevent))
				}
			}

			// Attempt to resolve event.
			results, err := ep.PersistEvent(ctx, *event)
			if err != nil {
				ep.Failed(0, *event, err)
			} else {
				for _, result := range results.Events {
					ep.Persisted(result)
				}
			}
		} else {
			log.Debug().Msg("Event persister received shutdown signal.")
			return
		}
	}
}
