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

package model

import (
	"time"
)

type EventType int64

// Enumeration of event types.
//go:generate stringer -type=EventType
const (
	NewRelationship EventType = iota
	Location
	Measurement
	Alert
	StateChange
	CommandInvocation
	CommandResponse
)

var EventTypesByName map[string]EventType

// Unresolved event details.
type UnresolvedEvent struct {
	Source        string
	AltId         *string
	Device        string
	Relationship  *string
	OccurredTime  time.Time
	ProcessedTime time.Time
	EventType     EventType
	Payload       interface{}
}

// Payload for creating a new device relationship.
type UnresolvedNewRelationshipPayload struct {
	DeviceRelationshipType string
	TargetDevice           *string
	TargetDeviceGroup      *string
	TargetAsset            *string
	TargetAssetGroup       *string
	TargetCustomer         *string
	TargetCustomerGroup    *string
	TargetArea             *string
	TargetAreaGroup        *string
}

// Information for a location entry.
type UnresolvedLocationEntry struct {
	Latitude     *string
	Longitude    *string
	Elevation    *string
	OccurredTime *string
}

// Payload creating new locations.
type UnresolvedLocationsPayload struct {
	Entries []UnresolvedLocationEntry
}

// Information for a measurements entry.
type UnresolvedMeasurementsEntry struct {
	Measurements map[string]string
	OccurredTime *string
}

// Payload creating new measurements.
type UnresolvedMeasurementsPayload struct {
	Entries []UnresolvedMeasurementsEntry
}

// Information for an alert entry.
type UnresolvedAlertEntry struct {
	Type         string
	Level        uint32
	Message      string
	Source       string
	OccurredTime *string
}

// Payload creating new alerts.
type UnresolvedAlertsPayload struct {
	Entries []UnresolvedAlertEntry
}

// Initializer.
func init() {
	EventTypesByName = make(map[string]EventType)
	EventTypesByName[NewRelationship.String()] = NewRelationship
	EventTypesByName[Location.String()] = Location
	EventTypesByName[Measurement.String()] = Measurement
	EventTypesByName[Alert.String()] = Alert
	EventTypesByName[StateChange.String()] = StateChange
	EventTypesByName[CommandInvocation.String()] = CommandInvocation
	EventTypesByName[CommandResponse.String()] = CommandResponse
}
