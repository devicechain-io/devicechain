// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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

	// Credential presented by the connecting device (ADR-014), carried so the
	// resolver can authenticate the device rather than trusting the Device token.
	CredentialType   *string `json:"credentialType,omitempty"`
	CredentialId     *string `json:"credentialId,omitempty"`
	CredentialSecret *string `json:"credentialSecret,omitempty"`
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
		return nil, fmt.Errorf("Unknown decoder type: %s", decodetype)
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

// Builds a new relationship payload from the json content. The target is a
// uniform (type, token) reference (ADR-013).
func (jd *JsonDecoder) BuildNewRelationshipPayload(source *JsonEvent) (*model.UnresolvedNewRelationshipPayload, error) {
	payload := &model.UnresolvedNewRelationshipPayload{}
	if rt, ok := source.Payload["relationshipType"]; ok {
		payload.RelationshipType = fmt.Sprintf("%v", rt)
	}
	if ttype, ok := source.Payload["targetType"]; ok {
		payload.TargetType = fmt.Sprintf("%v", ttype)
	}
	if target, ok := source.Payload["target"]; ok {
		payload.Target = fmt.Sprintf("%v", target)
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
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// Parses a measurements event.
func (jd *JsonDecoder) BuildMeasurementsPayload(source *JsonEvent) (*model.UnresolvedMeasurementsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedMeasurementsPayload{}
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// Parses an alerts event.
func (jd *JsonDecoder) BuildAlertsPayload(source *JsonEvent) (*model.UnresolvedAlertsPayload, error) {
	locbytes, err := json.Marshal(source.Payload)
	if err != nil {
		return nil, err
	}
	payload := &model.UnresolvedAlertsPayload{}
	if err := json.Unmarshal(locbytes, payload); err != nil {
		return nil, err
	}
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
		AltId:            jevent.AltId,
		Device:           jevent.Device,
		Relationship:     jevent.Relationship,
		CredentialType:   jevent.CredentialType,
		CredentialId:     jevent.CredentialId,
		CredentialSecret: jevent.CredentialSecret,
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
	case model.StateChange:
		// StateChange (presence, ADR-067) is a PLATFORM-PRODUCER event: a presence-
		// asserting adapter (the Sparkplug host) emits it directly over the wire
		// contract (proto), never through this device-facing JSON decoder. Accepting it
		// here would let any device credential forge its own presence — assert itself
		// permanently CONNECTED with an unbeatable session id, which the projection can
		// never supersede (the sweep skips ASSERTED and no data event flips it), hiding
		// the device's own death from monitoring. Reject it as an unsupported device event.
		return nil, nil, fmt.Errorf("state-change (presence) events are platform-produced and not accepted from device ingest")
	}

	return nil, nil, fmt.Errorf("unhandled event type: %s", jevent.EventType)
}
