// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The impact vocabulary of a command definition. Impact is how disruptive running the command
// is to what the device is doing; it decides whether the command waits for a maintenance
// window.
const (
	// ImpactNormal: a read or a benign write. Dispatches whenever the device is reachable.
	ImpactNormal = "NORMAL"
	// ImpactDisruptive: interrupts the device's work (a reboot). Held for the maintenance window.
	ImpactDisruptive = "DISRUPTIVE"
	// ImpactDestructive: erases state that cannot be recovered (a wipe). Held for the
	// maintenance window.
	ImpactDestructive = "DESTRUCTIVE"
)

// normalizeImpact reads a request's impact: empty means NORMAL, a known value is itself, and
// anything else is refused rather than stored — an impact nothing recognises would be read as
// NORMAL by the gate and let a disruptive command through.
func normalizeImpact(raw string) (string, error) {
	switch raw {
	case "":
		return ImpactNormal, nil
	case ImpactNormal, ImpactDisruptive, ImpactDestructive:
		return raw, nil
	}
	return "", fmt.Errorf("invalid impact %q: must be %s, %s or %s",
		raw, ImpactNormal, ImpactDisruptive, ImpactDestructive)
}

// EffectiveImpact is the definition's impact with the empty value read as NORMAL, which is
// what a definition frozen into a version published before the field existed carries.
func (d *CommandDefinition) EffectiveImpact() string {
	if d == nil || d.Impact == "" {
		return ImpactNormal
	}
	return d.Impact
}

// WindowGated reports whether a command of this impact waits for the maintenance window.
// It is the ONE definition of "gated", so the single gate, the batch gate and the console's
// preview cannot disagree.
func (d *CommandDefinition) WindowGated() bool {
	switch d.EffectiveImpact() {
	case ImpactDisruptive, ImpactDestructive:
		return true
	}
	return false
}

// MaintenanceWindow is a recurring weekly schedule, in an IANA time zone, during which
// disruptive operations may START. It is assigned to a device type.
//
// Slots are stored as one JSONB document (an array of maintwindow.Slot): they are read and
// validated as a whole, never queried by inner field, and their order carries nothing.
type MaintenanceWindow struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	// Timezone is an IANA zone name such as Europe/Berlin. Never empty and never "Local".
	Timezone string `gorm:"not null;size:64"`
	// Slots is the JSON array of weekly openings; see maintwindow.Slot.
	Slots datatypes.JSON `gorm:"not null"`
}

// DefaultOrder implements rdb.Sortable: newest first, token as the unique tiebreak.
func (MaintenanceWindow) DefaultOrder() string {
	return "maintenance_windows.created_at DESC, maintenance_windows.token ASC"
}

// Schedule builds the calculator for this window. An error means the stored document no
// longer validates (hand-edited row, or rules tightened since); the caller must treat that
// as a failure of the check, never as an open window.
func (w *MaintenanceWindow) Schedule() (*maintwindow.Schedule, error) {
	slots, err := decodeSlots(string(w.Slots))
	if err != nil {
		return nil, err
	}
	return maintwindow.New(w.Timezone, slots)
}

// Data required to create a maintenance window.
type MaintenanceWindowCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	// Timezone is an IANA zone name. Required.
	Timezone string
	// Slots is a JSON array of {day, start, durationMinutes}. Required, at least one.
	Slots    string
	Metadata *string
}

// MaintenanceWindowUpdateRequest is a PARTIAL update: omitted leaves a field alone, null
// clears a nullable one, a value sets it. Timezone and Slots are required, so an explicit
// null is refused. There is no Token — the mutation's own argument identifies the window.
type MaintenanceWindowUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	Timezone    dcgraphql.OptionalString
	Slots       dcgraphql.OptionalString
	Metadata    dcgraphql.OptionalString
}

// Search criteria for locating maintenance windows.
type MaintenanceWindowSearchCriteria struct {
	rdb.Pagination
}

// Results for maintenance window search.
type MaintenanceWindowSearchResults struct {
	Results    []MaintenanceWindow
	Pagination rdb.SearchResultsPagination
}
