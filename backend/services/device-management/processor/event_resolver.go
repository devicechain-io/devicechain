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
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/proto"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
)

// Worker used to resolve event entities.
type EventResolver struct {
	WorkerId   int
	Api        model.DeviceManagementApi
	Unresolved <-chan kafka.Message
	Invalid    func(error, kafka.Message)
	Resolved   func([]EventResolutionResults)
	Failed     func(uint, esmodel.UnresolvedEvent, error)
}

// Results of event resolution process.
type EventResolutionResults struct {
	Device       *model.Device
	Relationship *model.DeviceRelationship
	Resolved     *model.ResolvedEvent
}

// Create a new event resolver.
func NewEventResolver(workerId int, api model.DeviceManagementApi,
	unrez <-chan kafka.Message,
	invalid func(error, kafka.Message),
	resolved func([]EventResolutionResults),
	failed func(uint, esmodel.UnresolvedEvent, error)) *EventResolver {
	return &EventResolver{
		WorkerId:   workerId,
		Api:        api,
		Unresolved: unrez,
		Invalid:    invalid,
		Resolved:   resolved,
		Failed:     failed,
	}
}

// Merge device and relationship data with unresolved event in order to create a resolved event.
func (rez *EventResolver) MergeRelationshipToResolveEvent(device *model.Device, relation *model.DeviceRelationship,
	event *esmodel.UnresolvedEvent, rezPayload interface{}) (*EventResolutionResults, error) {
	// Assemble resolved event from initial event and device assignment.
	resolved := &model.ResolvedEvent{
		Source:                event.Source,
		AltId:                 event.AltId,
		SourceDeviceId:        device.ID,
		DeviceRelationshipId:  relation.ID,
		TargetDeviceId:        relation.TargetDeviceId,
		TargetDeviceGroupId:   relation.TargetDeviceGroupId,
		TargetAssetId:         relation.TargetAssetId,
		TargetAssetGroupId:    relation.TargetAssetGroupId,
		TargetCustomerId:      relation.TargetCustomerId,
		TargetCustomerGroupId: relation.TargetCustomerGroupId,
		TargetAreaId:          relation.TargetAreaId,
		TargetAreaGroupId:     relation.TargetAreaGroupId,
		OccurredTime:          event.OccurredTime,
		ProcessedTime:         event.ProcessedTime,
		EventType:             event.EventType,
		Payload:               rezPayload,
	}

	results := &EventResolutionResults{
		Device:       device,
		Relationship: relation,
		Resolved:     resolved,
	}

	return results, nil
}

// Create a new device relationship based on inbound event.
func (rez *EventResolver) CreateNewDeviceRelationship(ctx context.Context, device *model.Device,
	relcreate esmodel.UnresolvedNewRelationshipPayload) (*model.DeviceRelationship, uint, error) {
	create := &model.DeviceRelationshipCreateRequest{
		Token:            uuid.New().String(),
		SourceDevice:     device.Token,
		RelationshipType: relcreate.DeviceRelationshipType,
		Metadata:         nil,
		Targets: model.EntityRelationshipCreateRequest{
			TargetDevice:        relcreate.TargetDevice,
			TargetDeviceGroup:   relcreate.TargetDeviceGroup,
			TargetAsset:         relcreate.TargetAsset,
			TargetAssetGroup:    relcreate.TargetAssetGroup,
			TargetArea:          relcreate.TargetArea,
			TargetAreaGroup:     relcreate.TargetAreaGroup,
			TargetCustomer:      relcreate.TargetCustomer,
			TargetCustomerGroup: relcreate.TargetCustomerGroup,
		},
	}
	created, err := rez.Api.CreateDeviceRelationship(ctx, create)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}
	return created, 0, nil
}

// Handle a new relationship event.
func (rez *EventResolver) HandleNewRelationshipEvent(ctx context.Context,
	device *model.Device, event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	relcreate, ok := event.Payload.(*esmodel.UnresolvedNewRelationshipPayload)
	if !ok {
		return nil, uint(dmproto.FailureReason_Invalid), errors.New("new relationship payload was not of expected type")
	}

	// Create new device relationship from the event payload.
	created, reason, err := rez.CreateNewDeviceRelationship(ctx, device, *relcreate)
	if err != nil {
		return nil, reason, errors.New("could not create device assignment")
	}

	// Convert to resolved payload.
	payload := &model.ResolvedNewRelationshipPayload{
		DeviceRelationshipTypeId: uint64(created.ID),
		TargetDeviceId:           proto.NullUint64Of(created.TargetDeviceId),
		TargetDeviceGroupId:      proto.NullUint64Of(created.TargetDeviceGroupId),
		TargetAssetId:            proto.NullUint64Of(created.TargetAssetId),
		TargetAssetGroupId:       proto.NullUint64Of(created.TargetAssetGroupId),
		TargetCustomerId:         proto.NullUint64Of(created.TargetCustomerId),
		TargetCustomerGroupId:    proto.NullUint64Of(created.TargetCustomerGroupId),
		TargetAreaId:             proto.NullUint64Of(created.TargetAreaId),
		TargetAreaGroupId:        proto.NullUint64Of(created.TargetAreaGroupId),
	}

	// Merge info from device and created assignment into event.
	resolved, err := rez.MergeRelationshipToResolveEvent(device, created, event, payload)
	if err != nil {
		return nil, uint(dmproto.FailureReason_Unknown), errors.New("unable to merge info to resolve event")
	}

	return []EventResolutionResults{*resolved}, 0, nil
}

// Resolve a locations event payload.
func (rez *EventResolver) ResolveLocationsEventPayload(ctx context.Context, device *model.Device,
	relation *model.DeviceRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if lpayload, ok := event.Payload.(*esmodel.UnresolvedLocationsPayload); ok {
		rlpayload := &model.ResolvedLocationsPayload{}
		rlentries := make([]model.ResolvedLocationEntry, 0)
		for _, ulentry := range lpayload.Entries {
			rlentry := model.ResolvedLocationEntry{
				Latitude:     ulentry.Latitude,
				Longitude:    ulentry.Longitude,
				Elevation:    ulentry.Elevation,
				OccurredTime: ulentry.OccurredTime,
			}
			rlentries = append(rlentries, rlentry)
		}
		rlpayload.Entries = rlentries
		return rlpayload, nil
	}
	return nil, fmt.Errorf("can not resolve locations payload. invalid unresolved payload type")
}

// Resolve a measurements event payload.
func (rez *EventResolver) ResolveMeasurementsEventPayload(ctx context.Context, device *model.Device,
	relation *model.DeviceRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if mpayload, ok := event.Payload.(*esmodel.UnresolvedMeasurementsPayload); ok {
		rmpayload := &model.ResolvedMeasurementsPayload{}
		rmsentries := make([]model.ResolvedMeasurementsEntry, 0)
		for _, umsentry := range mpayload.Entries {
			rmentries := make([]model.ResolvedMeasurementEntry, 0)
			for mxkey, mxvalue := range umsentry.Measurements {
				rmentry := model.ResolvedMeasurementEntry{
					Name:       mxkey,
					Value:      mxvalue,
					Classifier: nil,
				}
				rmentries = append(rmentries, rmentry)
			}
			rmsentry := model.ResolvedMeasurementsEntry{
				Entries:      rmentries,
				OccurredTime: umsentry.OccurredTime,
			}
			rmsentries = append(rmsentries, rmsentry)
		}
		rmpayload.Entries = rmsentries
		return rmpayload, nil
	}
	return nil, fmt.Errorf("can not resolve measurements payload. invalid unresolved payload type")
}

// Resolve a alerts event payload.
func (rez *EventResolver) ResolveAlertsEventPayload(ctx context.Context, device *model.Device,
	relation *model.DeviceRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	if apayload, ok := event.Payload.(*esmodel.UnresolvedAlertsPayload); ok {
		rapayload := &model.ResolvedAlertsPayload{}
		raentries := make([]model.ResolvedAlertEntry, 0)
		for _, uaentry := range apayload.Entries {
			raentry := model.ResolvedAlertEntry{
				Type:         uaentry.Type,
				Level:        uaentry.Level,
				Message:      uaentry.Message,
				Source:       uaentry.Source,
				OccurredTime: uaentry.OccurredTime,
			}
			raentries = append(raentries, raentry)
		}
		rapayload.Entries = raentries
		return rapayload, nil
	}
	return nil, fmt.Errorf("can not resolve alerts payload. invalid unresolved payload type")
}

// Convert an unresolved event payload into a resolved payload.
func (rez *EventResolver) ResolveEventPayload(ctx context.Context, device *model.Device,
	relation *model.DeviceRelationship, event *esmodel.UnresolvedEvent) (interface{}, error) {
	switch event.EventType {
	case esmodel.Location:
		return rez.ResolveLocationsEventPayload(ctx, device, relation, event)
	case esmodel.Measurement:
		return rez.ResolveMeasurementsEventPayload(ctx, device, relation, event)
	case esmodel.Alert:
		return rez.ResolveAlertsEventPayload(ctx, device, relation, event)
	default:
		return nil, fmt.Errorf("unable to handle resolution for payload type: %s", event.EventType.String())
	}
}

// Create resolved events by looking up device assignment info and merging it into other event data.
func (rez *EventResolver) HandleStandardEvent(ctx context.Context,
	device *model.Device, event *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	// Look up device relationships for tracked types.
	tracked := true
	criteria := model.DeviceRelationshipSearchCriteria{
		Pagination: rdb.Pagination{
			PageNumber: 1,
			PageSize:   0,
		},
		SourceDevice: &device.Token,
		Tracked:      &tracked,
	}
	drels, err := rez.Api.DeviceRelationships(ctx, criteria)
	if err != nil {
		return nil, uint(dmproto.FailureReason_ApiCallFailed), err
	}

	// Create separate merged event for each tracked device relationship.
	results := make([]EventResolutionResults, 0)
	for _, drel := range drels.Results {
		resolved, err := rez.ResolveEventPayload(ctx, device, &drel, event)
		if err != nil {
			return nil, uint(dmproto.FailureReason_ApiCallFailed), err
		}

		result, err := rez.MergeRelationshipToResolveEvent(device, &drel, event, resolved)
		if err != nil {
			return nil, uint(dmproto.FailureReason_ApiCallFailed), err
		}
		results = append(results, *result)
	}

	return results, 0, nil
}

// Route event to handlers based on event type.
func (rez *EventResolver) HandleEvent(ctx context.Context,
	device *model.Device, unresolved *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	switch unresolved.EventType {
	case esmodel.NewRelationship:
		return rez.HandleNewRelationshipEvent(ctx, device, unresolved)
	case esmodel.Location, esmodel.Measurement, esmodel.Alert:
		return rez.HandleStandardEvent(ctx, device, unresolved)
	default:
		return nil, uint(dmproto.FailureReason_Invalid), fmt.Errorf("unhandled event type: %s", unresolved.EventType.String())
	}
}

// Execute logic to resolve event.
func (rez *EventResolver) ResolveEvent(ctx context.Context, unrez *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	matches, err := rez.Api.DevicesByToken(context.Background(), []string{unrez.Device})
	if err != nil || len(matches) == 0 {
		return nil, uint(dmproto.FailureReason_DeviceNotFound), err
	}
	return rez.HandleEvent(ctx, matches[0], unrez)
}

// Converts unresolved events into resolved events.
func (rez *EventResolver) Process(ctx context.Context) {
	for {
		unresolved, more := <-rez.Unresolved
		if more {
			log.Debug().Msg(fmt.Sprintf("Event resolution handled by resolver id %d", rez.WorkerId))

			// Attempt to unmarshal event.
			event, err := esproto.UnmarshalUnresolvedEvent(unresolved.Value)
			if err != nil {
				rez.Invalid(err, unresolved)
				continue
			}

			if log.Debug().Enabled() {
				jevent, err := json.MarshalIndent(event, "", "  ")
				if err == nil {
					log.Debug().Msg(fmt.Sprintf("Received %s event:\n%s", event.EventType.String(), jevent))
				}
			}

			// Attempt to resolve event.
			resolved, reason, err := rez.ResolveEvent(ctx, event)
			if err != nil {
				rez.Failed(reason, *event, err)
			} else {
				rez.Resolved(resolved)
			}
		} else {
			log.Debug().Msg("Event resolver received shutdown signal.")
			return
		}
	}
}
