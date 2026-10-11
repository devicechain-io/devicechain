// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// UpdateAssignment is one decision that a device should install an artifact: the append-only
// history of what a device was told. A row is never edited except to stamp SupersededAt when a
// newer assignment replaces it, and never deleted, so overwriting a target does not erase that
// the earlier one was ever assigned.
//
// The wire names an assignment by its Token (assignmentId), which is unique within the tenant.
//
// Field order mirrors the migration's snapshot struct; physical column order is part of the schema.
type UpdateAssignment struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	rdb.TenantScoped

	Token       string `gorm:"not null;size:128"`
	DeviceToken string `gorm:"not null;size:128;index"`
	Component   string `gorm:"not null;size:64"`
	ArtifactId  uint   `gorm:"not null;index"`
	Actor       string `gorm:"size:256"`

	// SupersededAt is set when a newer assignment replaced this one. Null means this is the
	// device's current assignment for the component.
	SupersededAt sql.NullTime
}

// AuditLabel names the row in the audit journal.
func (a UpdateAssignment) AuditLabel() string { return a.Token }

// DefaultOrder implements rdb.Sortable: newest first, token as the tiebreak so the order is total.
func (UpdateAssignment) DefaultOrder() string {
	return "update_assignments.created_at DESC, update_assignments.token ASC"
}

// UpdateAttempt is the persisted form of one ota.Attempt: the platform's view of one try at
// moving one component of one device to an assignment's artifact. The state machine itself lives
// in core/ota; this row only stores what it decided. See ToOTA and fromOTA for the mapping, and
// the migration for why Version exists.
type UpdateAttempt struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped

	Token        string `gorm:"not null;size:128"`
	AssignmentId uint   `gorm:"not null"`
	DeviceToken  string `gorm:"not null;size:128"`
	Component    string `gorm:"not null;size:64"`

	TargetVersion  string `gorm:"not null;size:64"`
	TargetDigest   string `gorm:"not null;size:71"`
	RequiresReboot bool   `gorm:"not null"`

	State        string    `gorm:"not null;size:16"`
	Version      int64     `gorm:"not null"`
	LastSeq      int64     `gorm:"not null"`
	LastReportAt time.Time `gorm:"not null"`

	BootIdAtStart   string `gorm:"not null;size:128"`
	InstallBootId   string `gorm:"not null;size:128"`
	LastBootId      string `gorm:"not null;size:128"`
	LastStage       string `gorm:"not null;size:16"`
	CancelRequested bool   `gorm:"not null"`
	Reconciled      bool   `gorm:"not null"`

	ProgressBytes sql.NullInt64
	ProgressTotal sql.NullInt64

	FailureStage    string `gorm:"not null;size:16"`
	FailureCode     string `gorm:"not null;size:64"`
	FailureDetail   string `gorm:"not null;size:512"`
	FailurePlatform bool   `gorm:"not null"`
}

// AuditLabel names the row in the audit journal.
func (a UpdateAttempt) AuditLabel() string { return a.Token }

// DefaultOrder implements rdb.Sortable.
func (UpdateAttempt) DefaultOrder() string {
	return "update_attempts.created_at DESC, update_attempts.token ASC"
}

// ToOTA reads the row as the value the reducer folds reports into.
func (a UpdateAttempt) ToOTA(assignmentToken string) ota.Attempt {
	out := ota.Attempt{
		AttemptID:    a.Token,
		AssignmentID: assignmentToken,
		Component:    a.Component,
		Target: ota.Target{
			Version: a.TargetVersion, Digest: a.TargetDigest, RequiresReboot: a.RequiresReboot,
		},
		State:           ota.State(a.State),
		LastSeq:         uint64(a.LastSeq),
		LastReportAt:    a.LastReportAt,
		BootIDAtStart:   a.BootIdAtStart,
		InstallBootID:   a.InstallBootId,
		LastBootID:      a.LastBootId,
		LastStage:       ota.ReportStage(a.LastStage),
		CancelRequested: a.CancelRequested,
		Reconciled:      a.Reconciled,
	}
	if a.ProgressBytes.Valid {
		out.Progress = &ota.Progress{Bytes: a.ProgressBytes.Int64, Total: a.ProgressTotal.Int64}
	}
	if a.FailureCode != "" || a.FailureStage != "" {
		out.Failure = &ota.Failure{Stage: ota.ReportStage(a.FailureStage), Code: a.FailureCode,
			Detail: a.FailureDetail, Platform: a.FailurePlatform}
	}
	return out
}

// otaColumns is the set of columns an ota.Attempt determines, as an update map. It is what a
// compare-and-set write stores; identity columns (token, device, component, target) never change.
func otaColumns(next ota.Attempt) map[string]any {
	cols := map[string]any{
		"state":            string(next.State),
		"last_seq":         int64(next.LastSeq),
		"last_report_at":   next.LastReportAt,
		"boot_id_at_start": next.BootIDAtStart,
		"install_boot_id":  next.InstallBootID,
		"last_boot_id":     next.LastBootID,
		"last_stage":       string(next.LastStage),
		"cancel_requested": next.CancelRequested,
		"reconciled":       next.Reconciled,
		"progress_bytes":   sql.NullInt64{},
		"progress_total":   sql.NullInt64{},
		"failure_stage":    "",
		"failure_code":     "",
		"failure_detail":   "",
		"failure_platform": false,
	}
	if p := next.Progress; p != nil {
		cols["progress_bytes"] = sql.NullInt64{Int64: p.Bytes, Valid: true}
		cols["progress_total"] = sql.NullInt64{Int64: p.Total, Valid: true}
	}
	if f := next.Failure; f != nil {
		cols["failure_stage"] = string(f.Stage)
		cols["failure_code"] = f.Code
		cols["failure_detail"] = f.Detail
		cols["failure_platform"] = f.Platform
	}
	return cols
}

// UpdateAssignmentView is an assignment with the artifact it names and its attempt, the shape the
// GraphQL plane returns.
type UpdateAssignmentView struct {
	Assignment UpdateAssignment
	Attempt    UpdateAttempt
	Artifact   Artifact
}

// UpdateAssignmentCriteria filters and paginates an assignment search.
type UpdateAssignmentCriteria struct {
	rdb.Pagination
	DeviceToken *string
	Component   *string
	// CurrentOnly restricts the search to each device's current assignment.
	CurrentOnly bool
}

// UpdateAssignmentResults is a page of assignments plus its pagination info.
type UpdateAssignmentResults struct {
	Results    []UpdateAssignmentView
	Pagination rdb.SearchResultsPagination
}
