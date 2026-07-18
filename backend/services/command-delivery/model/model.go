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
type CommandStatus string

const (
	CommandQueued CommandStatus = "QUEUED"
	CommandSent   CommandStatus = "SENT"
	// CommandDelivered is a RESERVED state: it models a device-confirmed delivery
	// distinct from a device response, but nothing emits it today because there is
	// no device delivery-acknowledgment transport (a device reply lands directly
	// as SUCCESSFUL/FAILED via MarkResponse). It is retained as a known/valid
	// status — the schema and read model already carry it — for when such an ack
	// exists; until then the effective lifecycle skips it (see canTransition).
	CommandDelivered  CommandStatus = "DELIVERED"
	CommandSuccessful CommandStatus = "SUCCESSFUL"
	CommandTimeout    CommandStatus = "TIMEOUT"
	CommandExpired    CommandStatus = "EXPIRED"
	CommandFailed     CommandStatus = "FAILED"
)

// Valid reports whether the status is one of the known lifecycle states.
func (s CommandStatus) Valid() bool {
	switch s {
	case CommandQueued, CommandSent, CommandDelivered,
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
	DeliveredTime   sql.NullTime
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
