// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// BatchTargetKind names how a batch's device set was specified.
type BatchTargetKind string

const (
	// BatchTargetDeviceList: the caller named the devices explicitly.
	BatchTargetDeviceList BatchTargetKind = "DEVICE_LIST"
	// BatchTargetGroup: the caller named an entity group, resolved at fire time.
	BatchTargetGroup BatchTargetKind = "GROUP"
)

func (k BatchTargetKind) String() string { return string(k) }

// maxStoredRefusalsPerCode bounds how many individual refusals a batch record keeps for
// any one code.
//
// 🔴 IT IS A BOUND ON A TENANT-CONTROLLED WRITE. A dynamic group can legally resolve to
// tens of thousands of devices, and under allowPartial every device over the headroom is a
// refusal — so an unbounded list would let one mutation write an arbitrarily large blob
// onto a single row. Truncating the SAMPLE while keeping RefusalCounts complete keeps the
// record's arithmetic closed (resolved = accepted + total refusals) and still gives a human
// enough device tokens to act on. A hundred names is already more than anyone pastes into a
// retry by hand.
const maxStoredRefusalsPerCode = 100

// BatchDeviceRefusal names one device a batch did not enqueue to, and why. It is the same
// (code, reason) pair a single enqueue rejection carries, plus the device it is about,
// because a batch answers about many devices at once.
type BatchDeviceRefusal struct {
	DeviceToken string        `json:"deviceToken"`
	Code        RejectionCode `json:"code"`
	Reason      string        `json:"reason"`
}

// RefusalCount is the COMPLETE total for one refusal code, never truncated — the
// counterweight to the bounded sample in Refusals.
type RefusalCount struct {
	Code  RejectionCode `json:"code"`
	Count int           `json:"count"`
}

// CommandBatch is one command fanned out to many devices (ADR-012).
//
// It exists as a persisted record rather than only a mutation response because the response
// is consumed once. A refused device is deliberately given no command row — there is no
// state that means "wanted but not created", and inventing one would repeat the deleted
// DELIVERED mistake of carrying a status nothing writes — so without this record a refusal
// leaves no trace anywhere, and an operator who fires a fleet push and comes back in the
// morning has nothing to read.
//
// 🔑 Resolved and Accepted are STORED FACTS about the moment the batch fired, not live
// counts. Command rows are not immortal (soft delete, tenant purge), so deriving Accepted
// from a query would let it drift below the creation-time truth with no refusal explaining
// the gap — and `Resolved == Accepted + total refusals`, the invariant that makes this
// record self-auditing, would quietly stop holding. Live delivery state is a different
// question, answered by searching commands on BatchToken.
type CommandBatch struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	// The command every member receives. One key and one payload for the whole batch is
	// what makes the enqueue gate affordable at fleet scale.
	Name    string
	Payload *datatypes.JSON

	// How the target was named, and — for a group — which group and which FROZEN version
	// actually resolved. The version is what lets an audit answer "what did this group
	// MEAN when this fired?" after someone edits the selector.
	TargetKind   string
	GroupToken   sql.NullString
	GroupVersion sql.NullInt32

	// Whether the caller accepted a partial fan-out.
	AllowPartial bool

	Resolved int
	Accepted int

	// A bounded sample of refusals, and the complete per-code totals.
	Refusals      *datatypes.JSON
	RefusalCounts *datatypes.JSON

	// When someone called this batch off, and how many commands that call caught.
	//
	// 🔑 BOTH ARE STORED FACTS ABOUT THE MOMENT OF THE CANCEL, on the same doctrine as
	// Resolved and Accepted above, and CancelledCount is NOT a live count of the batch's
	// CANCELLED rows. The two drift apart legitimately: a command that was already SENT
	// when the cancel ran is driven to CANCELLED later, if its publish fails and the
	// release finds the batch stamped. Deriving this number instead would make it move
	// after the fact, and an audit could no longer answer "what did pulling the brake
	// actually stop?"
	//
	// CancelledAt is also the flag ReleaseClaim reads to decide whether a failed publish
	// retires its command or returns it to the queue. It does that from a LATER
	// transaction, once this one has committed — the two writes land together, and no
	// other session can see either until they do.
	CancelledAt    sql.NullTime
	CancelledCount sql.NullInt32
}

// DefaultOrder implements rdb.Sortable. Same reasoning as Command: the batch list is the
// console's "what did I just fire?" surface, and the console does no client-side
// re-sorting, so newest-first here is the product-visible ordering rather than only a
// paging fix.
//
// created_at alone is not total — two batches fired from a script land in the same clock
// tick — so token ASC is the tiebreak. Both columns are NOT NULL (gorm.Model and
// rdb.TokenReference respectively), so NULLS placement does not apply.
func (CommandBatch) DefaultOrder() string {
	return "command_batches.created_at DESC, command_batches.token ASC"
}

// CommandBatchCreateRequest carries the data required to issue a batch.
//
// DeviceTokens and GroupToken are alternatives: exactly one must be supplied. Both or
// neither is a rejection rather than a precedence rule — a caller that sent both does not
// know what it asked for, and silently preferring one would fan a physical actuation out
// across whichever set the implementation happened to favour.
type CommandBatchCreateRequest struct {
	Token     string
	Name      string
	Payload   *string
	ExpiresAt *string
	Metadata  *string
	// DeviceTokens is a POINTER to a slice because this struct is bound directly as a
	// GraphQL input, and the library requires a pointer for a nullable input field —
	// the same reason CommandSearchCriteria.Statuses is shaped this way. Read it
	// through NamedDevices, which is nil-safe.
	DeviceTokens *[]string
	GroupToken   *string
	GroupVersion *int32
	AllowPartial bool
}

// NamedDevices is the explicitly-named device list, or empty when the request targets a
// group. Absent and empty are deliberately the same thing here: "exactly one target"
// is decided on whether a list has ENTRIES, so a caller sending `deviceTokens: []`
// alongside a group is naming a group, not naming both.
func (r *CommandBatchCreateRequest) NamedDevices() []string {
	if r.DeviceTokens == nil {
		return nil
	}
	return *r.DeviceTokens
}
