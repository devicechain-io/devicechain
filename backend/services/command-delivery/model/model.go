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
// A command moves QUEUED -> SENT -> SUCCESSFUL on the happy path. The four
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
//   - PARKED — dispatched, and the transport found the device unreachable, so it
//     went NOWHERE. The platform still holds it and will deliver it on the
//     device's next wake.
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
// 🔴 PARKED EXISTS BECAUSE SENT WAS TELLING THAT EXACT LIE, IN THE ONE CASE THIS
// COMMENT DID NOT ANTICIPATE. The platform decides whether to publish from
// PRESENCE — is the device registered — but only a transport knows REACHABILITY,
// and for a queue-mode device (an LwM2M sleeper) those differ: it is registered
// and asleep. The sweep published, the transport found no live connection, and
// the row stayed SENT. So SENT carried two opposite meanings — "the device has
// it" and "it went nowhere" — and everything downstream assumed the first. That
// one ambiguity produced four separate defects: a cancelled fleet write still
// actuated on the next wake; the cancel reported such rows as beyond recall when
// they were fully recallable; a command that reached nothing expired as TIMEOUT,
// blaming the device; and the drain re-dispatched from SENT without claiming,
// which was the platform's only unclaimed dispatch.
//
// So SENT now means exactly what this comment always said it meant, and PARKED
// carries the other meaning. Note what SENT still does NOT promise: it is
// "dispatched toward a device believed live", not "the device was reached" — an
// MQTT device dropping between the presence read and the publish still lands
// here, because that transport is live-only with no acknowledgment.
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
	CommandParked     CommandStatus = "PARKED"
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
	CommandQueued, CommandHeld, CommandSent, CommandParked,
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
	// BatchToken is the same link denormalized, and it is what lets the command search
	// filter by batch without a join, since the criteria builder is single-table
	// (CommandSearchCriteria.BatchToken).
	//
	// 🔑 THAT SEARCH IS THE ONLY WAY TO ASK WHAT A BATCH IS DOING NOW. The batch record's
	// Resolved and Accepted are stored facts about the moment it fired, deliberately not
	// live counts — so "how many of those 5,000 have actually been delivered?" is a
	// question only the command rows can answer.
	BatchId    sql.NullInt64
	BatchToken sql.NullString

	// DispatchNonce identifies the CURRENT dispatch attempt. Every write that moves a row
	// into SENT — the sweep's claim and the wake drain's claim alike — stamps a fresh value,
	// and the published delivery envelope carries it.
	//
	// 🔴 IT EXISTS TO MAKE PARKING AT-MOST-ONCE PER DISPATCH, AND WITHOUT IT PARKING WOULD
	// RE-ARM A COMMAND THE DEVICE HAD ALREADY RUN. SENT used to be a one-way door: nothing
	// outside this service could move a row backwards, which is why the drain was allowed to
	// dispatch from it without claiming. ParkClaim opens that door on purpose, and JetStream
	// redelivery then walks through it:
	//
	//	1. the transport parks the row, and its ACK IS LOST;
	//	2. the device wakes; the drain claims PARKED -> SENT and ACTUATES;
	//	3. at AckWait the ORIGINAL message redelivers, finds the device offline again, and a
	//	   park predicated only on `status = 'SENT'` MATCHES the freshly-actuated row;
	//	4. the next wake claims and actuates it a SECOND time.
	//
	// The transport's in-process dedup cannot cover this: its TTL is of the same order as
	// AckWait, it is per-pod, and the amplifying case is a leadership failover — a cold cache
	// and a whole-fleet re-register storm at the same moment.
	//
	// ⚠️ "of the same order", not "exactly", and the weakening is a correction. This comment
	// used to claim the dedup TTL WAS AckWait. The two numbers are equal today, but nothing
	// holds them together: the dedup TTL is sized from the op timeout (see dedupeTTL in the
	// LwM2M dispatcher) and AckWait from worker-pipeline latency, so either can move without
	// the other. Do not "restore" the exact claim by wiring one constant to the other either
	// — that would fuse two independently motivated values so a change to one silently
	// retunes the other. The argument below does not need the equality.
	//
	// Predicating the park on the nonce it
	// was handed closes it by construction: the stale message names a dispatch that no longer
	// exists, matches zero rows, and is acked as settled.
	//
	// It is NOT a delivery counter and must never be read as one. Comparing two values tells
	// you only "same dispatch" or "not the same dispatch"; nothing orders them.
	DispatchNonce sql.NullString
}

// DefaultOrder implements rdb.Sortable. Newest-first is the PRODUCT requirement here,
// not merely a determinism fix: the command list backs the batch-command console
// surface, whose dominant use is "I just fired a fleet write — show it to me", and the
// console does no client-side re-sorting (its data table has no sorted row model), so
// server order IS display order.
//
// created_at is not unique — every row of one batch shares a single instant — so token
// ASC carries the totality rule. Both columns are NOT NULL (created_at from gorm.Model,
// token from rdb.TokenReference), so no NULLS placement is needed.
//
// 🔑 THAT SHARED INSTANT IS ENFORCED, NOT ASSUMED, AND THE DIFFERENCE WAS A LIVE DEFECT.
// This comment used to assert it as an obvious fact ("a batch inserts thousands of rows
// inside one clock tick"). It was false: CreateInBatches issues one INSERT per chunk and
// gorm stamps once per STATEMENT, so a 2,500-device batch carried three timestamps and
// read back in reverse chunk order. insertBatchCommands now presets created_at for the
// whole batch, which is what makes the sentence above true. Anything that inserts
// commands in bulk must do the same, or this order silently stops meaning what it says.
//
// Within a batch, token ASC is not an arbitrary tiebreak: batchCommandToken is
// zero-padded on the device's position, so it reproduces ADMITTED order — the order the
// caller gave, and the order a partially-admitted batch honours.
func (Command) DefaultOrder() string {
	return "commands.created_at DESC, commands.token ASC"
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
// caller cares about are usually a SET (the LwM2M wake drain wants HELD ∪ PARKED:
// the commands withheld because the device was known absent, plus those a transport
// published and handed back on finding nobody there), and expressing that as
// repeated single-status queries costs a round trip per state and cannot be paged
// coherently.
//
// 🔴 SENT IS NOT IN THAT SET, AND THIS NOTE USED TO SAY IT WAS. A sent command is
// at the device; re-dispatching it on the next wake actuates hardware a second
// time. That is the defect PARKED was added to fix — see the status doc above.
//
// An EMPTY Statuses slice is treated as "no status filter", not as "match
// nothing". Both readings are defensible; this one is chosen because a caller
// that builds a status list programmatically and ends up with none of them
// almost always means "I have no preference", and the other reading turns that
// into a silently empty page.
//
// ⚠️ An earlier version of this note justified the choice by claiming gorm
// renders an empty IN as a NULL comparison "whose result is neither", i.e. that
// the behaviour was driver-dependent. Measured, it is not: `status IN ?` with an
// empty slice returns zero rows, deterministically and without error. The choice
// is ours to defend on its own terms, which is what the paragraph above now does.
type CommandSearchCriteria struct {
	rdb.Pagination
	DeviceToken *string
	Status      *string
	Statuses    *[]string
	// BatchToken narrows to the commands one batch created. It is the live view of a
	// fleet write — the batch record itself carries only creation-time counts — so it
	// is what answers "of the 5,000 this batch queued, how many have gone out?" when
	// combined with Status or Statuses.
	BatchToken *string
}

// CommandSearchResults wraps a page of commands.
type CommandSearchResults struct {
	Results    []Command
	Pagination rdb.SearchResultsPagination
}
