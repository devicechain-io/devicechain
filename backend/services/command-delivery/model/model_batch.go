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
}

// CommandBatchCreateRequest carries the data required to issue a batch.
//
// DeviceTokens and GroupToken are alternatives: exactly one must be supplied. Both or
// neither is a rejection rather than a precedence rule — a caller that sent both does not
// know what it asked for, and silently preferring one would fan a physical actuation out
// across whichever set the implementation happened to favour.
type CommandBatchCreateRequest struct {
	Token        string
	Name         string
	Payload      *string
	ExpiresAt    *string
	Metadata     *string
	DeviceTokens []string
	GroupToken   *string
	GroupVersion *int32
	AllowPartial bool
}

// BatchCancellation reports what a batch cancel could and could not stop.
//
// 🔴 IT IS A BREAKDOWN RATHER THAN A COUNT, and that is the whole point. Cancelling a batch
// is a panic operation — the operator has just realised they fired the wrong thing at a
// fleet — and the only question that matters is how much of it already went out. A single
// number cannot distinguish "stopped all 5,000" from "stopped 3,800, and 1,200 machines are
// already acting on it", which are opposite answers to the question actually being asked.
type BatchCancellation struct {
	// Cancelled is how many commands were stopped before dispatch (QUEUED or HELD).
	Cancelled int64
	// AlreadyInFlight is how many had already been published toward a device (SENT) and
	// were therefore NOT cancelled. These stay governed by their TTL and by whatever the
	// device answers.
	AlreadyInFlight int64
	// AlreadyTerminal is how many had already finished, one way or another.
	AlreadyTerminal int64
}
