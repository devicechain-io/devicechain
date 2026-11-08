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

// CampaignElementStatus is where one device's step in one cycle stands. It is the CAMPAIGN's
// bookkeeping; what the command then did on the device is the command's own status, read
// from the commands the campaign's batches created.
type CampaignElementStatus string

const (
	// ElementPending: planned, not yet due.
	ElementPending CampaignElementStatus = "PENDING"
	// ElementClaimed: due, taken by a pass, and its batch token assigned; the batch is about
	// to be (or is being) created. A claimed element a dead replica left behind is picked up
	// again, because creating a batch is idempotent on its token.
	ElementClaimed CampaignElementStatus = "CLAIMED"
	// ElementDispatched: the batch admitted the device and wrote its command.
	ElementDispatched CampaignElementStatus = "DISPATCHED"
	// ElementRefused: the batch did not admit the device (vocabulary, ceiling, window...).
	ElementRefused CampaignElementStatus = "REFUSED"
	// ElementSkipped: the device was never commanded, and Reason says why (NO_SITE,
	// POLAR_DAY, POLAR_NIGHT, MISSED, CANCELLED, PREDATES_CAMPAIGN, DISABLED, SUPERSEDED,
	// NOT_IN_GROUP).
	ElementSkipped CampaignElementStatus = "SKIPPED"
)

// Skip reasons. They are a closed vocabulary because an operator filters on them.
const (
	SkipNoSite     = "NO_SITE"
	SkipPolarDay   = "POLAR_DAY"
	SkipPolarNight = "POLAR_NIGHT"
	// SkipMissed: the element fell due and was not claimed within the campaign's
	// lateness bound (an outage, or the campaign was disabled). Firing a dusk command at
	// midnight would be worse than not firing it.
	SkipMissed = "MISSED"
	// SkipPredatesCampaign: the element fell due before the campaign was created. A solar plan
	// starts at the previous local midnight, so its first cycles reach back before the campaign
	// existed; firing those would command an event the campaign never covered (and would
	// re-fire, after a delete and recreate, a dusk the old campaign already commanded).
	SkipPredatesCampaign = "PREDATES_CAMPAIGN"
	// SkipWhileDisabled: the element fell due while the campaign was switched off. Switching it
	// back on starts from then; it does not release what it would have sent meanwhile.
	SkipWhileDisabled = "DISABLED"
	// SkipSuperseded: a newer firing of the same step for the same device was due at the same
	// time, and only the newest is sent. After an outage, ten missed hourly firings are one
	// command, not a burst of ten.
	SkipSuperseded = "SUPERSEDED"
	// SkipNotInGroup: the device had left the target group by the time its cycle fell due.
	SkipNotInGroup = "NOT_IN_GROUP"
)

// CampaignElementStatuses is the closed vocabulary of element statuses, for filtering.
var CampaignElementStatuses = []CampaignElementStatus{
	ElementPending, ElementClaimed, ElementDispatched, ElementRefused, ElementSkipped,
}

// CampaignTargetGroup is the only target kind this build resolves: an entity group of devices.
const CampaignTargetGroup = "GROUP"

// Campaign is a scheduled, tracked fan-out of one or more command steps to the devices of a
// group (the Job of the platform's job-and-campaign model).
//
// A Campaign is a DEFINITION. Each time its trigger fires it plans a CampaignCycle, and a
// cycle holds one CampaignElement per device per step, so "which lights did not dim last
// night, and why" is a query rather than a log search.
type Campaign struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference

	Name    string
	Enabled bool

	// Trigger is the core/schedule Spec, as JSON. Steps is the ordered []CampaignStep.
	Trigger datatypes.JSON
	Steps   datatypes.JSON

	TargetKind string
	GroupToken string

	// MaxLatenessSeconds bounds how late an element may fire; ExpiresInSeconds is the
	// time-to-live stamped on the commands it creates, counted from creation.
	MaxLatenessSeconds int
	ExpiresInSeconds   int

	// PlannedThrough is the instant up to which cycles have been planned. It only moves
	// forward, and only after the cycles it covers are stored.
	PlannedThrough sql.NullTime
	// LastError is why the last planning attempt failed, cleared by the next success.
	LastError sql.NullString
	// EnabledAt is when the campaign was last switched on (its creation, if it was created on).
	// An element due before it fell due while the campaign was off and is never sent.
	EnabledAt sql.NullTime
}

// DefaultOrder implements rdb.Sortable.
func (Campaign) DefaultOrder() string {
	return "campaigns.created_at DESC, campaigns.token ASC"
}

// CampaignCycle is one firing of a campaign.
type CampaignCycle struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	rdb.TenantScoped

	CampaignId uint
	// CycleKey identifies the firing for good (see schedule.Cycle.Key).
	CycleKey string
	// Day is the civil date of a solar cycle; At the fire instant of a fixed one.
	Day sql.NullString
	At  sql.NullTime

	GroupVersion sql.NullInt32
	// Resolved is how many devices the group held when the cycle was planned.
	Resolved int
	// MembershipCheckedAt is when the group was resolved again as the cycle first fell due, and
	// the devices that had left it skipped. Null until then; a cycle is not sent before it.
	MembershipCheckedAt sql.NullTime
}

// CampaignElement is one device's step in one cycle: the unit of tracking and of retry.
type CampaignElement struct {
	ID        uint `gorm:"primarykey"`
	UpdatedAt time.Time
	rdb.TenantScoped

	CampaignId  uint
	CycleId     uint
	Step        int
	DeviceToken string
	DueAt       time.Time

	Status string
	Reason sql.NullString

	// BatchToken is the batch that carried the command; RetryBatchToken the batch a
	// retry-from-failed later re-sent it in.
	BatchToken      sql.NullString
	RetryBatchToken sql.NullString

	// ClaimedBy is the pass that claimed the element, ClaimedAt when.
	ClaimedBy sql.NullString
	ClaimedAt sql.NullTime
}

// CampaignStep is one step of a campaign.
type CampaignStep struct {
	// Type is COMMAND. ARTIFACT (an update artifact) is a recognised member of the
	// vocabulary that this build does not execute, and is refused at create, never dropped.
	Type string `json:"type"`
	// Name and Payload are the command key and its JSON payload.
	Name    string `json:"name,omitempty"`
	Payload string `json:"payload,omitempty"`
	// DelaySeconds is how long after the trigger the step fires, per device.
	DelaySeconds int `json:"delaySeconds,omitempty"`
}

const (
	StepCommand  = "COMMAND"
	StepArtifact = "ARTIFACT"
)
