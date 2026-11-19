// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "github.com/devicechain-io/dc-device-management/contract"

// The wire contract this service shares with its consumers lives in package contract, so a
// consumer (event-processing, notification-management, ...) depends on the shapes it reads and
// not on this package. Everything below is a re-export, under the name this package has always
// used for it, so device-management's own code reads exactly as it did before the move. The
// types are ALIASES, not new types: model.AlarmStateChangeEvent and
// contract.AlarmStateChangeEvent are one type, with one method set and one encoding.
//
// Nothing here may be a definition. A shape that crosses a service boundary is defined in
// contract and only named here; a shape defined here is device-management's own.

// Alarm state-change facts (the alarm-events stream).
type (
	AlarmEventType        = contract.AlarmEventType
	AlarmStateChangeEvent = contract.AlarmStateChangeEvent
)

const (
	AlarmEventRaised                  = contract.AlarmEventRaised
	AlarmEventEscalated               = contract.AlarmEventEscalated
	AlarmEventDeescalated             = contract.AlarmEventDeescalated
	AlarmEventCleared                 = contract.AlarmEventCleared
	AlarmEventAcknowledged            = contract.AlarmEventAcknowledged
	AlarmClearReasonOriginatorDeleted = contract.AlarmClearReasonOriginatorDeleted
)

// The raise-alarm request event-processing's REACT dispatcher sends to this service.
type RaiseAlarmRequest = contract.RaiseAlarmRequest

const (
	AlarmEdgeRaised   = contract.AlarmEdgeRaised
	AlarmEdgeResolved = contract.AlarmEdgeResolved
)

// MarshalRaiseAlarmRequest is contract.MarshalRaiseAlarmRequest.
func MarshalRaiseAlarmRequest(req *RaiseAlarmRequest) ([]byte, error) {
	return contract.MarshalRaiseAlarmRequest(req)
}

// UnmarshalRaiseAlarmRequest is contract.UnmarshalRaiseAlarmRequest.
func UnmarshalRaiseAlarmRequest(encoded []byte) (*RaiseAlarmRequest, error) {
	return contract.UnmarshalRaiseAlarmRequest(encoded)
}

// The detection-rules-published, device-roster, device-attribute and ownership-changed facts.
type (
	PublishedDetectionRule       = contract.PublishedDetectionRule
	DetectionRulesPublishedEvent = contract.DetectionRulesPublishedEvent
	DeviceRosterEvent            = contract.DeviceRosterEvent
	DeviceAttributeEvent         = contract.DeviceAttributeEvent
	DeviceOwnershipChangedEvent  = contract.DeviceOwnershipChangedEvent
)

// The reconcile doors event-processing repairs its detection projections against.
type (
	ActiveProfileRules       = contract.ActiveProfileRules
	DeviceRosterEntry        = contract.DeviceRosterEntry
	DeviceThresholdAttribute = contract.DeviceThresholdAttribute
)

const (
	MaxActiveProfileRulesPageSize = contract.MaxActiveProfileRulesPageSize
	MaxRosterPageSize             = contract.MaxRosterPageSize
	MaxThresholdAttributePageSize = contract.MaxThresholdAttributePageSize
)

// SameRuleDefinition is contract.SameRuleDefinition.
func SameRuleDefinition(a, b string) bool {
	return contract.SameRuleDefinition(a, b)
}

// The fence-set manifest fact and the geometry door it is resolved through.
type (
	GeoFenceSetManifest   = contract.GeoFenceSetManifest
	GeoFenceManifestEntry = contract.GeoFenceManifestEntry
)

const MaxGeoFenceGeometryHashesPerRequest = contract.MaxGeoFenceGeometryHashesPerRequest

// GeoFenceGeometryHash is contract.GeoFenceGeometryHash.
func GeoFenceGeometryHash(document []byte) string {
	return contract.GeoFenceGeometryHash(document)
}

// MaxGeoFenceSetManifestBytes is contract.MaxGeoFenceSetManifestBytes.
func MaxGeoFenceSetManifestBytes() int {
	return contract.MaxGeoFenceSetManifestBytes()
}

// MarshalGeoFenceSetManifest is contract.MarshalGeoFenceSetManifest.
func MarshalGeoFenceSetManifest(manifest *GeoFenceSetManifest) ([]byte, error) {
	return contract.MarshalGeoFenceSetManifest(manifest)
}

// UnmarshalGeoFenceSetManifest is contract.UnmarshalGeoFenceSetManifest.
func UnmarshalGeoFenceSetManifest(encoded []byte) (*GeoFenceSetManifest, error) {
	return contract.UnmarshalGeoFenceSetManifest(encoded)
}

// The attribute scope vocabulary the device-attribute fact carries.
type AttributeScope = contract.AttributeScope

const (
	AttributeScopeClient = contract.AttributeScopeClient
	AttributeScopeServer = contract.AttributeScopeServer
	AttributeScopeShared = contract.AttributeScopeShared
)

// The enqueue gate's machine-readable rejection codes.
type EnqueueRejectionCode = contract.EnqueueRejectionCode

const (
	RejectDeviceNotFound            = contract.RejectDeviceNotFound
	RejectCommandNotInVocabulary    = contract.RejectCommandNotInVocabulary
	RejectPayloadSchemaViolation    = contract.RejectPayloadSchemaViolation
	RejectDeviceDisabled            = contract.RejectDeviceDisabled
	RejectCommandDestructiveRefused = contract.RejectCommandDestructiveRefused
	RejectWindowAfterExpiry         = contract.RejectWindowAfterExpiry
)
