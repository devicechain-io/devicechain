// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// CommandStatus is the lifecycle state of a persisted command (ADR-012).
// A command moves QUEUED -> SENT -> SUCCESSFUL on the happy
// path; the terminal states are SUCCESSFUL / TIMEOUT / EXPIRED / FAILED. No
// transition is permitted out of a terminal state.
//
// There is deliberately no DELIVERED state. Confirming delivery distinctly from
// a response needs a device- or broker-level acknowledgment, and no such
// transport exists: a device reply lands directly as SUCCESSFUL/FAILED via
// MarkResponse. A DELIVERED state was carried here through the schema, the API
// and the console for a long time with nothing able to emit it, which read as a
// guarantee the platform did not make. If an ack transport is ever built, add
// the state back then — with something that writes it.
type CommandStatus string

const (
	CommandQueued     CommandStatus = "QUEUED"
	CommandSent       CommandStatus = "SENT"
	CommandSuccessful CommandStatus = "SUCCESSFUL"
	CommandTimeout    CommandStatus = "TIMEOUT"
	CommandExpired    CommandStatus = "EXPIRED"
	CommandFailed     CommandStatus = "FAILED"
)

// Valid reports whether the status is one of the known lifecycle states.
func (s CommandStatus) Valid() bool {
	switch s {
	case CommandQueued, CommandSent,
		CommandSuccessful, CommandTimeout, CommandExpired, CommandFailed:
		return true
	}
	return false
}

// Terminal reports whether the status is a terminal state (no further
// transition allowed): SUCCESSFUL / TIMEOUT / EXPIRED / FAILED.
func (s CommandStatus) Terminal() bool {
	switch s {
	case CommandSuccessful, CommandTimeout, CommandExpired, CommandFailed:
		return true
	}
	return false
}

// String returns the status as a string.
func (s CommandStatus) String() string {
	return string(s)
}

// Command is a persisted, lifecycle-tracked command to a device (NOT
// fire-and-forget). It targets a device by its connection token (which also
// addresses the delivery subject); identity is decoupled from device-management.
type Command struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	DeviceToken     string
	Name            string
	Payload         *datatypes.JSON
	Status          string
	QueuedTime      time.Time
	SentTime        sql.NullTime
	RespondedTime   sql.NullTime
	ExpiresAt       sql.NullTime
	ResponsePayload *datatypes.JSON
	Error           sql.NullString
}

// CommandCreateRequest carries the data required to issue a command.
type CommandCreateRequest struct {
	Token       string
	DeviceToken string
	Name        string
	Payload     *string
	ExpiresAt   *string
	Metadata    *string
}

// CommandSearchCriteria is the search criteria for locating commands.
type CommandSearchCriteria struct {
	rdb.Pagination
	DeviceToken *string
	Status      *string
}

// CommandSearchResults wraps a page of commands.
type CommandSearchResults struct {
	Results    []Command
	Pagination rdb.SearchResultsPagination
}
