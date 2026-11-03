// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import "unicode/utf8"

// Why a measurement entry was discarded at resolution while the rest of its event was stored.
// A closed vocabulary shared by every side of the seam: the producer (device-management's
// resolver, which also uses the strings as the resolve_measurement_dropped_total label), the
// consumer that projects the record (device-state) and the GraphQL enum that serves it, so a
// reason added here is a reason every one of them has to name.
const (
	// DropUndeclaredNonNumeric: the key is not on the profile and its value is not a number
	// (a text value such as "ok"), so it cannot go in the numeric measurement column.
	DropUndeclaredNonNumeric = "undeclared_non_numeric"
	// DropUndeclaredUnstorable: the key is not on the profile and its value is numeric but
	// cannot be stored (NaN, +/-Inf, or beyond decimal(20,8)).
	DropUndeclaredUnstorable = "undeclared_unstorable"
	// DropDeclaredNotStorable: the key is declared with a data type that is not a time-series
	// metric (defensive: creating such a definition is already refused).
	DropDeclaredNotStorable = "declared_not_storable"
)

// DroppedReasons is every reason, in a stable order.
var DroppedReasons = []string{DropUndeclaredNonNumeric, DropUndeclaredUnstorable, DropDeclaredNotStorable}

// MaxDroppedValueBytes bounds the value a drop record carries. The record is a hint about WHICH
// key lost data, not a copy of the data, so a long text value is cut.
const MaxDroppedValueBytes = 64

// MaxDroppedNameBytes bounds the key a drop record carries: the width of the column that holds it
// (the same as a stored reading's name), so a longer key cannot make the consumer's write fail.
const MaxDroppedNameBytes = 128

// DroppedMeasurementEntry is one measurement entry the resolver discarded from a sample while it
// stored the sample's other entries.
//
// It is a diagnostic for the device-state projection and nothing else: it is not persisted with
// the event and it never reaches event identity (ResolvedMeasurementsPayload.IdentityView drops
// it), so a drop cannot move an event id.
type DroppedMeasurementEntry struct {
	Name   string
	Reason string
	// Value is the value the device sent, truncated to MaxDroppedValueBytes.
	Value string
}

// TruncateDroppedValue cuts v to MaxDroppedValueBytes on a UTF-8 boundary, so the result is
// still a valid string (the wire refuses one that is not).
func TruncateDroppedValue(v string) string {
	if len(v) <= MaxDroppedValueBytes {
		return v
	}
	cut := MaxDroppedValueBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}

// TruncateDroppedName cuts a key to MaxDroppedNameBytes on a UTF-8 boundary.
func TruncateDroppedName(n string) string {
	if len(n) <= MaxDroppedNameBytes {
		return n
	}
	cut := MaxDroppedNameBytes
	for cut > 0 && !utf8.RuneStart(n[cut]) {
		cut--
	}
	return n[:cut]
}
