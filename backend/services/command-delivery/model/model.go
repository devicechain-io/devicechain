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
//
// A command moves QUEUED -> SENT -> SUCCESSFUL on the happy path. The three
// non-terminal states each answer a different question about a command that has
// not finished, and keeping them distinct is the point of the vocabulary:
//
//   - QUEUED — accepted; awaiting its FIRST dispatch decision. Genuinely
//     transient: every row leaves it at the next sweep tick.
//   - HELD   — the platform is deliberately WITHHOLDING dispatch because the
//     device is known absent. This is where an offline fleet's backlog
//     accumulates, and it can sit for days.
//   - SENT   — dispatched toward a device believed reachable; awaiting its
//     response.
//
// The distinction is not cosmetic. A single "not finished yet" value cannot
// answer "is this stuck?", cannot be counted for a backlog ceiling (a healthy
// online fleet must count zero), and cannot tell an operator WHY a command has
// not arrived. It also decides the right terminal when a TTL elapses: a HELD
// command that lapses never went out (EXPIRED), while a SENT one did and was
// never answered (TIMEOUT) — see ExpireStale. Collapsing them reports a machine
// that was switched off all weekend as "sent, never answered", which is a lie
// about the device rather than a fact about the platform.
//
// The terminal states are SUCCESSFUL / FAILED / TIMEOUT / EXPIRED / CANCELLED.
// No transition is permitted out of a terminal state. CANCELLED is its own
// terminal because cancellation is a different ACTOR, not a different outcome:
// EXPIRED means "the platform ran out of time to send it", CANCELLED means "a
// human or a tenant called it off". These were one value for a long time, which
// is why the docs had to carry the apology "cancelling a command also records
// EXPIRED".
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
	CommandHeld       CommandStatus = "HELD"
	CommandSent       CommandStatus = "SENT"
	CommandSuccessful CommandStatus = "SUCCESSFUL"
	CommandTimeout    CommandStatus = "TIMEOUT"
	CommandExpired    CommandStatus = "EXPIRED"
	CommandCancelled  CommandStatus = "CANCELLED"
	CommandFailed     CommandStatus = "FAILED"
)

// terminalStatuses is the ONE definition of "no further transition allowed".
//
// 🔴 Everything that needs the terminal set derives it from here: Terminal()
// below, and terminalStatusStrings() in api.go, which builds the "status NOT IN
// (…)" guard shared by MarkResponse, CancelCommand and both of ExpireStale's
// writes. Those two used to be independent lists, and independent lists of the
// same thing drift: a state added to one and missed in the other produces a row
// that the fast-path guard treats as finished while the SQL guard still lets a
// sweep overwrite it — no error, no test failure, just a cancelled command that
// silently becomes TIMEOUT.
var terminalStatuses = []CommandStatus{
	CommandSuccessful, CommandFailed,
	CommandTimeout, CommandExpired, CommandCancelled,
}

// nonTerminalStatuses is the complement, in lifecycle order. Declared rather
// than derived so that adding a state forces a decision about which side it
// lands on; Valid() is the two lists joined, so a state omitted from BOTH reads
// as unknown rather than silently defaulting to non-terminal.
var nonTerminalStatuses = []CommandStatus{
	CommandQueued, CommandHeld, CommandSent,
}

// Valid reports whether the status is one of the known lifecycle states.
func (s CommandStatus) Valid() bool {
	for _, known := range nonTerminalStatuses {
		if s == known {
			return true
		}
	}
	return s.Terminal()
}

// Terminal reports whether the status is a terminal state (no further
// transition allowed): SUCCESSFUL / FAILED / TIMEOUT / EXPIRED / CANCELLED.
//
// An UNKNOWN status is deliberately reported non-terminal: a row carrying a
// value this build does not recognise must stay reachable by the sweep rather
// than be frozen forever. (TestCommandStatusTerminal pins that direction.)
func (s CommandStatus) Terminal() bool {
	for _, t := range terminalStatuses {
		if s == t {
			return true
		}
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

	// BatchId links a command to the CommandBatch that created it; null for a command
	// issued one at a time.
	//
	// It is also what keeps the two TOKEN NAMESPACES apart. A command token is a
	// client-chosen idempotency key, but a batch's commands carry tokens the platform
	// minted, in the same column — so the replay probe keys on `batch_id IS NULL` to avoid
	// answering a client's request with somebody else's actuation.
	//
	// BatchToken is the same link denormalized. It will let the command search filter by
	// batch without a join, since the criteria builder is single-table. ⚠️ That filter is
	// NOT built yet — CommandSearchCriteria has no batch field — so today this column is
	// carried for a reader that is still to come, and the claim it is load-bearing would
	// be false if made now.
	BatchId    sql.NullInt64
	BatchToken sql.NullString
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
//
// Status and Statuses are independent filters, ANDed like every other criterion
// — a caller supplying both gets the intersection, which for a single-value
// Status either matches or returns nothing. Statuses exists because the states a
// caller cares about are usually a SET (the LwM2M wake drain wants HELD ∪ SENT:
// the commands withheld for an absent device plus those already dispatched and
// still unanswered), and expressing that as repeated single-status queries costs
// a round trip per state and cannot be paged coherently.
//
// An EMPTY Statuses slice is treated as "no status filter", not as "match
// nothing". Both readings are defensible; this one is chosen because gorm
// renders an empty IN as a NULL-comparison whose result is neither, and a filter
// whose meaning depends on the driver is worse than either answer.
type CommandSearchCriteria struct {
	rdb.Pagination
	DeviceToken *string
	Status      *string
	Statuses    *[]string
}

// CommandSearchResults wraps a page of commands.
type CommandSearchResults struct {
	Results    []Command
	Pagination rdb.SearchResultsPagination
}
