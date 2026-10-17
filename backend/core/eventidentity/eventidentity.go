// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventidentity is the seam between a resolved payload and the bytes an event id is
// derived from.
//
// A payload that carries values which are NOT part of what a device sent (calculated metrics,
// ADR-082) must leave them out of its event id, or one reading would have two ids whenever a
// formula changed between two copies of it. The payload types live in device-management and the
// id is derived in event-management; this package lets the first describe its identity view and
// the second hash it without event-management's storage model importing device-management's
// model package (and, through it, the formula compiler).
package eventidentity

import "reflect"

// Viewer is implemented by a payload that hashes as something other than itself.
// IdentityPayload returns the value to canonicalize (json.Marshal) in place of the payload.
//
// A payload with nothing to leave out MUST return itself unchanged, so that it hashes
// byte-for-byte as it did before the view existed; every stored event id was derived that way.
type Viewer interface {
	IdentityPayload() any
}

// Payload returns what the event id of payload is derived from: its identity view when it has
// one, otherwise payload itself. A nil pointer is returned as is (it has no view to take, and
// marshals as null either way).
func Payload(payload any) any {
	v, ok := payload.(Viewer)
	if !ok {
		return payload
	}
	if rv := reflect.ValueOf(payload); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return payload
	}
	return v.IdentityPayload()
}
