// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// ActiveProfileRulesPage is one keyset page of ActiveProfileRules. NextCursor is the last row id
// the page scanned when the page was full, and nil when the walk is complete.
type ActiveProfileRulesPage struct {
	Entries    []ActiveProfileRules
	NextCursor *uint64
}

// DeviceRosterPage is one keyset page of the device roster.
type DeviceRosterPage struct {
	Entries    []DeviceRosterEntry
	NextCursor *uint64
}

// DeviceThresholdAttributePage is one keyset page of threshold attributes. A page can hold fewer
// entries than it scanned rows (a non-numeric row is scanned and skipped), so an empty page with
// a cursor is NOT the end of the walk; only a nil cursor is.
type DeviceThresholdAttributePage struct {
	Entries    []DeviceThresholdAttribute
	NextCursor *uint64
}

// DisabledDevicePage is one keyset page of the tokens of the devices an operator has disabled.
// NextCursor is the last device id the page scanned when it scanned a full page, and nil when the
// walk is complete.
type DisabledDevicePage struct {
	Tokens     []string
	NextCursor *uint64
}
