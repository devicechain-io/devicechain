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
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
)

const (
	DECODER_TYPE_JSON = "json"
)

// Payload expected for events passed in json format.
type JsonEvent struct {
	AltId        *string                `json:"altId,omitempty"`
	Device       string                 `json:"device"`
	Relationship *string                `json:"relationship,omitempty"`
	OccurredTime *string                `json:"occurredTime,omitempty"`
	EventType    string                 `json:"eventType"`
	Payload      map[string]interface{} `json:"payload"`
}

// Interface implemented by all decoders.
type Decoder interface {
	// Decodes a binary payload into an event.
	Decode(payload []byte) (*model.UnresolvedEvent, interface{}, error)
}

// Create a new decoder based on the given type indicator.
func NewDecoderForType(decodetype string, config map[string]string) (Decoder, error) {
	switch decodetype {
	case DECODER_TYPE_JSON:
		return NewJsonDecoder(config), nil
	default:
		return nil, fmt.Errorf(fmt.Sprintf("Unknown decoder type: %s", decodetype))
	}
}

// Decodes payloads that use json format.
type JsonDecoder struct {
	Configuration map[string]string
}

// Create a new json decoder.
func NewJsonDecoder(config map[string]string) *JsonDecoder {
	return &JsonDecoder{
		Configuration: config,
	}
}

// Builds a new relationship payload from the json content.
func (jd *JsonDecoder) BuildNewRelationshipPayload(source *JsonEvent) (*model.UnresolvedNewRelationshipPayload, error) {
	payload := &model.UnresolvedNewRelationshipPayload{}
	if drt, ok := source.Payload["deviceRelationshipType"]; ok {
		str := fmt.Sprintf("%v", drt)
		payload.DeviceRelationshipType = str
	}
	if device, ok := source.Payload["targetDevice"]; ok {
		str := fmt.Sprintf("%v", device)
		payload.TargetDevice = &str
	}
	if dgroup, ok := source.Payload["targetDeviceGroup"]; ok {
		str := fmt.Sprintf("%v", dgroup)
		payload.TargetDeviceGroup = &str
	}
	if asset, ok := source.Payload["targetAsset"]; ok {
		str := fmt.Sprintf("%v", asset)
		payload.TargetAsset = &str
	}
	if agroup, ok := source.Payload["targetAssetGroup"]; ok {
		str := fmt.Sprintf("%v", agroup)
		payload.TargetAssetGroup = &str
	}
	if cust, ok := source.Payload["targetCustomer"]; ok {
		str := fmt.Sprintf("%v", cust)
		payload.TargetCustomer = &str
	}
	if cgroup, ok := source.Payload["targetCustomerGroup"]; ok {
		str := fmt.Sprintf("%v", cgroup)
		payload.TargetCustomerGroup = &str
	}
	if area, ok := source.Payload["targetArea"]; ok {
		str := fmt.Sprintf("%v", area)
		payload.TargetArea = &str
	}
	if agroup, ok := source.Payload["targetAreaGroup"]; ok {
		str := fmt.Sprintf("%v", agroup)
		payload.TargetAreaGroup = &str
	}

	return payload, nil
}

// Parses a locations event.
func (jd *JsonDecoder) BuildLocationsPayload(source *JsonEvent) (*model.UnresolvedLocationsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedLocationsPayload{}
	json.Unmarshal(locbytes, payload)
	return payload, nil
}

// Parses a measurements event.
func (jd *JsonDecoder) BuildMeasurementsPayload(source *JsonEvent) (*model.UnresolvedMeasurementsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedMeasurementsPayload{}
	json.Unmarshal(locbytes, payload)
	return payload, nil
}

// Parses an alerts event.
func (jd *JsonDecoder) BuildAlertsPayload(source *JsonEvent) (*model.UnresolvedAlertsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedAlertsPayload{}
	json.Unmarshal(locbytes, payload)
	return payload, nil
}

// Parse json event payload.
func (jd *JsonDecoder) ParseEvent(payload []byte) (*JsonEvent, error) {
	jevent := &JsonEvent{}
	err := json.Unmarshal(payload, jevent)
	if err != nil {
		return nil, err
	}
	return jevent, nil
}

// Assemble an event based on json event data.
func (jd *JsonDecoder) AssembleEvent(jevent *JsonEvent) (*model.UnresolvedEvent, error) {
	event := &model.UnresolvedEvent{
		AltId:        jevent.AltId,
		Device:       jevent.Device,
		Relationship: jevent.Relationship,
	}
	if etype, ok := model.EventTypesByName[jevent.EventType]; ok {
		event.EventType = etype
	} else {
		return nil, fmt.Errorf("unknown event type in json payload: %s", jevent.EventType)
	}
	if jevent.OccurredTime != nil {
		otime, err := time.Parse(time.RFC3339, *jevent.OccurredTime)
		if err != nil {
			return nil, err
		}
		event.OccurredTime = otime
	} else {
		event.OccurredTime = time.Now()
	}
	event.ProcessedTime = time.Now()
	return event, nil
}

// Decode a json payload into an event.
func (jd *JsonDecoder) Decode(payload []byte) (*model.UnresolvedEvent, interface{}, error) {
	// Parse json payload.
	jevent, err := jd.ParseEvent(payload)
	if err != nil {
		return nil, nil, err
	}
	// Assemble event from json data.
	event, err := jd.AssembleEvent(jevent)
	if err != nil {
		return nil, nil, err
	}

	// Create payload based on event type.
	switch event.EventType {
	case model.NewRelationship:
		payload, err := jd.BuildNewRelationshipPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Location:
		payload, err := jd.BuildLocationsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Measurement:
		payload, err := jd.BuildMeasurementsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	case model.Alert:
		payload, err := jd.BuildAlertsPayload(jevent)
		if err != nil {
			return nil, nil, err
		}
		return event, payload, nil
	}

	return nil, nil, fmt.Errorf("unhandled event type: %s", jevent.EventType)
}
