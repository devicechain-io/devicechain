// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Rollout states. DRAFT is declared and editable by nobody (there is nothing to edit yet); RUNNING
// is being driven; PAUSED keeps its place; COMPLETED and ABORTED are terminal.
const (
	RolloutDraft     = "DRAFT"
	RolloutRunning   = "RUNNING"
	RolloutPaused    = "PAUSED"
	RolloutCompleted = "COMPLETED"
	RolloutAborted   = "ABORTED"
)

// RolloutStates is the closed vocabulary of rollout states.
var RolloutStates = []string{RolloutDraft, RolloutRunning, RolloutPaused, RolloutCompleted, RolloutAborted}

// Why a rollout is PAUSED. Empty while it is not.
const (
	PauseOperator         = "OPERATOR"
	PauseFailureThreshold = "FAILURE_THRESHOLD"
	// PauseArtifactNotAssignable: the artifact was retired, or lost the trust this instance
	// requires (its only active signer was revoked), while the rollout was running.
	PauseArtifactNotAssignable = "ARTIFACT_NOT_ASSIGNABLE"
)

// What the rollout decided about one target device. The device's OUTCOME is not one of these: it
// is read from the attempt the assignment names (see Outcome*).
const (
	TargetPending    = "PENDING"
	TargetDispatched = "DISPATCHED"
	TargetSkipped    = "SKIPPED"
	TargetCancelled  = "CANCELLED"
)

// Outcomes of one target, as the progress read model reports them. Every one but PENDING, SKIPPED
// and (for a target the abort reached first) CANCELLED is derived from the attempt row.
const (
	OutcomePending   = "PENDING"
	OutcomeInFlight  = "IN_FLIGHT"
	OutcomeSucceeded = "SUCCEEDED"
	OutcomeFailed    = "FAILED"
	OutcomeUnknown   = "UNKNOWN"
	OutcomeCancelled = "CANCELLED"
	OutcomeSkipped   = "SKIPPED"
)

// Outcomes is the closed vocabulary of target outcomes.
var Outcomes = []string{OutcomePending, OutcomeInFlight, OutcomeSucceeded, OutcomeFailed, OutcomeUnknown,
	OutcomeCancelled, OutcomeSkipped}

// UpdateRollout is one operator decision to move a group of devices to an artifact, at most
// MaxInFlight of them at a time.
//
// Field order mirrors the migration's snapshot struct; physical column order is part of the schema.
type UpdateRollout struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped

	Token            string `gorm:"not null;size:128"`
	Name             string `gorm:"not null;size:128"`
	ArtifactId       uint   `gorm:"not null;index"`
	GroupToken       string `gorm:"not null;size:128"`
	GroupVersion     sql.NullInt32
	MaxInFlight      int `gorm:"not null"`
	FailureThreshold sql.NullInt32
	State            string `gorm:"not null;size:16"`
	PauseReason      string `gorm:"not null;size:32"`
	TargetCount      int    `gorm:"not null"`
	CreatedBy        string `gorm:"size:256"`
	StartedAt        sql.NullTime
	FinishedAt       sql.NullTime
	StateChangedBy   string `gorm:"size:256"`
	StateChangedAt   sql.NullTime
}

// AuditLabel names the row in the audit journal.
func (r UpdateRollout) AuditLabel() string { return r.Token }

// DefaultOrder implements rdb.Sortable: newest first, token as the tiebreak so the order is total.
func (UpdateRollout) DefaultOrder() string {
	return "update_rollouts.created_at DESC, update_rollouts.token ASC"
}

// UpdateRolloutTarget is one device of a rollout's frozen target set.
type UpdateRolloutTarget struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped

	RolloutId    uint   `gorm:"not null"`
	DeviceToken  string `gorm:"not null;size:128"`
	Status       string `gorm:"not null;size:16"`
	AssignmentId sql.NullInt64
	DispatchedAt sql.NullTime
	SkipCode     string `gorm:"not null;size:64"`
	SkipDetail   string `gorm:"not null;size:512"`
}

// AuditLabel names the row in the audit journal.
func (t UpdateRolloutTarget) AuditLabel() string { return t.DeviceToken }

// DefaultOrder implements rdb.Sortable: the order the targets were frozen in.
func (UpdateRolloutTarget) DefaultOrder() string { return "update_rollout_targets.id ASC" }

// outcomeOf classifies a target from what the rollout decided and, for a dispatched one, the state
// of the attempt its assignment names. A dispatched target whose attempt cannot be found is IN
// FLIGHT: it keeps its slot rather than freeing capacity on a row that is not there.
func outcomeOf(status string, attemptState sql.NullString) string {
	switch status {
	case TargetPending:
		return OutcomePending
	case TargetSkipped:
		return OutcomeSkipped
	case TargetCancelled:
		return OutcomeCancelled
	}
	if !attemptState.Valid {
		return OutcomeInFlight
	}
	switch ota.State(attemptState.String) {
	case ota.StateUpdated:
		return OutcomeSucceeded
	case ota.StateFailed, ota.StateTimedOut:
		return OutcomeFailed
	case ota.StateCancelled:
		return OutcomeCancelled
	case ota.StateUnknown:
		return OutcomeUnknown
	}
	return OutcomeInFlight
}

// RolloutProgress counts a rollout's targets by outcome.
type RolloutProgress struct {
	Total, Pending, InFlight, Succeeded, Failed, Unknown, Cancelled, Skipped int
}

func (p *RolloutProgress) add(outcome string, n int) {
	p.Total += n
	switch outcome {
	case OutcomePending:
		p.Pending += n
	case OutcomeInFlight:
		p.InFlight += n
	case OutcomeSucceeded:
		p.Succeeded += n
	case OutcomeFailed:
		p.Failed += n
	case OutcomeUnknown:
		p.Unknown += n
	case OutcomeCancelled:
		p.Cancelled += n
	case OutcomeSkipped:
		p.Skipped += n
	}
}

// Occupied is how many targets hold a slot against MaxInFlight: in flight, and UNKNOWN (a device
// that may have installed and not come back must not free capacity for the next one).
func (p RolloutProgress) Occupied() int { return p.InFlight + p.Unknown }

// Failures is what the failure threshold counts: attempts that ended FAILED or TIMED_OUT, and
// those whose outcome is UNKNOWN, which must not read as healthy.
func (p RolloutProgress) Failures() int { return p.Failed + p.Unknown }

// UpdateRolloutView is a rollout with its artifact and progress, the shape the GraphQL plane returns.
type UpdateRolloutView struct {
	Rollout  UpdateRollout
	Artifact Artifact
	Progress RolloutProgress
}

// RolloutTargetView is one target with its derived outcome and, once dispatched, the assignment
// token and attempt state.
type RolloutTargetView struct {
	Target          UpdateRolloutTarget
	Outcome         string
	AssignmentToken string
	AttemptState    string
}

// RolloutCreateRequest declares a rollout.
type RolloutCreateRequest struct {
	Token            string
	Name             string
	ArtifactToken    string
	GroupToken       string
	MaxInFlight      int
	FailureThreshold *int
}

// RolloutSearchCriteria filters and paginates a rollout search.
type RolloutSearchCriteria struct {
	rdb.Pagination
	State *string
}

// RolloutSearchResults is a page of rollouts.
type RolloutSearchResults struct {
	Results    []UpdateRolloutView
	Pagination rdb.SearchResultsPagination
}

// RolloutTargetCriteria filters and paginates a rollout's targets.
type RolloutTargetCriteria struct {
	rdb.Pagination
	Outcome *string
}

// RolloutTargetResults is a page of targets.
type RolloutTargetResults struct {
	Results    []RolloutTargetView
	Pagination rdb.SearchResultsPagination
}
