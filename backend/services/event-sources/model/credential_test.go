// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

// The three fixture markers below stand in for the credential fields. They are
// deliberately NOT shaped like a credential: every assertion here fails by printing
// what it found, and a test that proves credential material is dropped should not be
// the thing that writes credential-shaped text into a CI log.
const (
	markerType   = "fixture-type-marker"
	markerId     = "fixture-id-marker"
	markerSecret = "fixture-secret-marker"
)

// eventWithCredentialMarkers is an event carrying all three credential fields.
func eventWithCredentialMarkers() UnresolvedEvent {
	ctype, cid, secret := markerType, markerId, markerSecret
	return UnresolvedEvent{
		Source:           "a-source",
		Device:           "a-device",
		EventType:        Measurement,
		CredentialType:   &ctype,
		CredentialId:     &cid,
		CredentialSecret: &secret,
	}
}

// All three credential fields are gone from the returned copy. All three, not just
// the two obviously-secret ones: CredentialType arrives on device-controlled input
// like the others, so its contents are whatever a producer put there.
func TestWithoutPresentedCredentialClearsEveryField(t *testing.T) {
	got := eventWithCredentialMarkers().WithoutPresentedCredential()

	if got.CredentialType != nil {
		t.Errorf("CredentialType survived: %q", *got.CredentialType)
	}
	if got.CredentialId != nil {
		t.Errorf("CredentialId survived: %q", *got.CredentialId)
	}
	if got.CredentialSecret != nil {
		t.Errorf("CredentialSecret survived: %q", *got.CredentialSecret)
	}
}

// Nothing else about the event moves. Without this the "no credential material"
// assertion above would be satisfied by a method that returned a zero event, which
// would empty every dead-letter record on the way to satisfying it.
func TestWithoutPresentedCredentialKeepsEverythingElse(t *testing.T) {
	original := eventWithCredentialMarkers()

	got := original.WithoutPresentedCredential()

	if got.Source != original.Source {
		t.Errorf("Source = %q, want %q", got.Source, original.Source)
	}
	if got.Device != original.Device {
		t.Errorf("Device = %q, want %q", got.Device, original.Device)
	}
	if got.EventType != original.EventType {
		t.Errorf("EventType = %v, want %v", got.EventType, original.EventType)
	}
}

// The caller's own event is untouched.
//
// This is the counterweight to the clear, and it is the property that makes the
// clear safe to call anywhere: authentication reads these same fields, so a variant
// that cleared them in place could disarm the check downstream of wherever it was
// called. A copy cannot.
func TestWithoutPresentedCredentialDoesNotMutateTheOriginal(t *testing.T) {
	original := eventWithCredentialMarkers()

	_ = original.WithoutPresentedCredential()

	if original.CredentialType == nil || *original.CredentialType != markerType {
		t.Error("the original event's CredentialType was cleared by the copy")
	}
	if original.CredentialId == nil || *original.CredentialId != markerId {
		t.Error("the original event's CredentialId was cleared by the copy")
	}
	if original.CredentialSecret == nil || *original.CredentialSecret != markerSecret {
		t.Error("the original event's CredentialSecret was cleared by the copy")
	}
}
