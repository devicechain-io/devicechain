// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"
)

// The fleet-operation journals as the service reads and writes them. Their DDL is the appended
// migration schema.NewFleetOperationsSchema, which declares its own literal copy of every shape
// below; TestFleetOperationModelsMatchTheMigration keeps the two in step, and a change to a model
// is a new appended migration, never an edit of that one.
//
// Nothing writes these tables yet: the engine, the activation trigger and the served API follow.
// There is deliberately no foreign key anywhere (a journal must not block or cascade a device
// delete) and the names are not part of any served schema.
//
// Audit: the operation header AND the contracts (approver, authorities, step-up: who approved what)
// are audited like every other control-plane entity. The runtime row (pacer, heartbeat, holds:
// written many times a minute), the lines (one write per device, with their own state machine) and
// the events opt out, as the configuration report history does. The events exemption is a
// deliberate deviation from the slice spec: the events are themselves the append-only journal of
// every control transition and verdict, so auditing them would record each fact twice.

type FleetOperation struct {
	ID        uint      `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"index:idx_fleet_operations_tenant_created,priority:2"`
	UpdatedAt time.Time
	TenantId  string `gorm:"not null;size:128;uniqueIndex:uix_fleet_operations_tenant_token,priority:1;index:idx_fleet_operations_tenant_dispatch,priority:1;index:idx_fleet_operations_tenant_created,priority:1"`

	Token string `gorm:"not null;size:128;uniqueIndex:uix_fleet_operations_tenant_token,priority:2"`
	Kind  string `gorm:"not null;size:64"`
	Name  string `gorm:"not null;size:128"`

	Dispatch       string `gorm:"not null;size:16;index:idx_fleet_operations_tenant_dispatch,priority:2;check:chk_fleet_operations_dispatch,dispatch IN ('PLANNING','READY','RUNNING','PAUSED','DISPATCHED','ABORTED','DRAFT')"`
	Evidence       string `gorm:"not null;size:8;default:OPEN;check:chk_fleet_operations_evidence,evidence IN ('OPEN','CLOSED')"`
	PauseReason    string `gorm:"not null;size:32;default:''"`
	ExecutionEpoch int64  `gorm:"not null;default:0"`

	RequestDigest   string `gorm:"not null;size:71"`
	PlanDigest      string `gorm:"not null;size:71;default:''"`
	ContractVersion int32  `gorm:"not null;default:1"`
	ContractDigest  string `gorm:"not null;size:71;default:''"`

	// DriverProtocol is the lowest engine protocol that may drive this operation. Every
	// driver claim, reconciler pass, deadline pass and retention pass filters on it, so a pod
	// of an older release never works an operation a newer release defined.
	DriverProtocol int32 `gorm:"not null;check:chk_fleet_operations_driver_protocol,driver_protocol >= 1"`

	Intent      string `gorm:"not null;type:text"`
	TargetKind  string `gorm:"not null;size:16"`
	TargetCount int32  `gorm:"not null"`

	MaxPostsPerSecond     int32 `gorm:"not null"`
	EffPostsPerSecond     int32 `gorm:"not null"`
	FailureThreshold      sql.NullInt32
	EffFailureThreshold   sql.NullInt32
	AmbiguityThreshold    sql.NullInt32
	EffAmbiguityThreshold sql.NullInt32
	SettleDeadlineS       int32 `gorm:"not null"`

	Initiator        string `gorm:"not null;size:320"`
	Grantee          string `gorm:"not null;size:320;default:''"`
	GrantBoundAt     sql.NullTime
	ReversalOf       sql.NullInt64
	BrokerFailClosed bool `gorm:"not null;default:false"`

	ExpiresAt     sql.NullTime
	StartedAt     sql.NullTime
	DispatchedAt  sql.NullTime
	ClosedAt      sql.NullTime
	LinesPurgedAt sql.NullTime

	PostedLineCount   int32 `gorm:"not null;default:0"`
	ReversedLineCount int32 `gorm:"not null;default:0"`

	CreatedBy      sql.NullString `gorm:"size:320"`
	StateChangedBy sql.NullString `gorm:"size:320"`
	StateChangedAt sql.NullTime
}

func (FleetOperation) TableName() string { return "fleet_operations" }

type FleetOperationRuntime struct {
	OperationId        int64  `gorm:"primaryKey;autoIncrement:false;type:bigint"`
	TenantId           string `gorm:"not null;size:128"`
	PaceWindowStart    sql.NullTime
	PaceIssued         int32 `gorm:"not null;default:0"`
	ControlRequestedAt sql.NullTime
	DriverSeenAt       sql.NullTime
	LastProgressAt     sql.NullTime
	HoldCode           string `gorm:"not null;size:32;default:''"`
	HoldSince          sql.NullTime
	HoldSeenAt         sql.NullTime
	LastErrorCode      string `gorm:"not null;size:64;default:''"`
	LastErrorAt        sql.NullTime
}

func (FleetOperationRuntime) TableName() string { return "fleet_operation_runtime" }

type FleetOperationContract struct {
	ID          uint   `gorm:"primarykey"`
	TenantId    string `gorm:"not null;size:128"`
	OperationId int64  `gorm:"not null;type:bigint;uniqueIndex:uix_fleet_operation_contracts_version,priority:1"`
	Version     int32  `gorm:"not null;uniqueIndex:uix_fleet_operation_contracts_version,priority:2"`
	Digest      string `gorm:"not null;size:71"`
	Body        string `gorm:"not null;type:text"`

	ApprovalKind  string         `gorm:"not null;size:16"`
	PolicyVersion int32          `gorm:"not null"`
	ProposedBy    string         `gorm:"not null;size:320"`
	ApprovedBy    sql.NullString `gorm:"size:320"`
	Authorities   sql.NullString `gorm:"size:256"`
	ActorSession  sql.NullString `gorm:"size:128"`
	StepUpAt      sql.NullTime
	ApprovedAt    sql.NullTime
	ExpiresAt     sql.NullTime
	ActivatedAt   sql.NullTime
}

func (FleetOperationContract) TableName() string { return "fleet_operation_contracts" }

type FleetOperationLine struct {
	ID          uint   `gorm:"primarykey"`
	TenantId    string `gorm:"not null;size:128;index:idx_fleet_operation_lines_tenant_target,priority:1"`
	OperationId int64  `gorm:"not null;type:bigint;uniqueIndex:uix_fleet_operation_lines_ordinal,priority:1;index:idx_fleet_operation_lines_settlement,priority:1"`
	Ordinal     int32  `gorm:"not null;uniqueIndex:uix_fleet_operation_lines_ordinal,priority:2;index:idx_fleet_operation_lines_settlement,priority:3"`

	TargetKey     string        `gorm:"not null;size:128;default:''"`
	TargetId      sql.NullInt64 `gorm:"type:bigint;index:idx_fleet_operation_lines_tenant_target,priority:2"`
	RefusedAtPlan bool          `gorm:"not null;default:false"`

	Posting    string         `gorm:"not null;size:24;check:chk_fleet_operation_lines_posting,posting IN ('PLANNED','POSTED','REFUSED','SKIPPED','CANCELLED','DISPATCHING','POSTING_UNKNOWN')"`
	Settlement sql.NullString `gorm:"size:24;index:idx_fleet_operation_lines_settlement,priority:2;check:chk_fleet_operation_lines_settlement,settlement IS NULL OR (posting = 'POSTED' AND settlement IN ('CONFIRMED','FAILED','SUPERSEDED','AMBIGUOUS','RESOLVED_CONFIRMED','RESOLVED_FAILED'))"`
	Code       string         `gorm:"not null;size:64;default:''"`
	Detail     string         `gorm:"not null;size:512;default:''"`

	Prior          sql.NullString `gorm:"type:text;check:chk_fleet_operation_lines_prior,prior IS NULL OR posting = 'POSTED'"`
	PostedValue    sql.NullString `gorm:"type:text"`
	PostedRevision sql.NullInt64
	PostedEpoch    sql.NullInt64

	ReversesLineId  sql.NullInt64 `gorm:"type:bigint"`
	OverrideChanged bool          `gorm:"not null;default:false"`

	Attempts      int32 `gorm:"not null;default:0"`
	NextAttemptAt sql.NullTime
	LastErrorCode string `gorm:"not null;size:64;default:''"`

	PostedAt   sql.NullTime
	SettleBy   sql.NullTime
	SettledAt  sql.NullTime
	RedactedAt sql.NullTime

	EvidenceBasis    sql.NullString `gorm:"size:16"`
	EvidenceRevision sql.NullInt64
	EvidenceFlag     string `gorm:"not null;size:40;default:''"`

	ResolutionId sql.NullInt64  `gorm:"type:bigint"`
	ResolvedBy   sql.NullString `gorm:"size:320"`
	ResolvedAt   sql.NullTime
}

func (FleetOperationLine) TableName() string { return "fleet_operation_lines" }

type FleetOperationEvent struct {
	ID          uint          `gorm:"primarykey;index:idx_fleet_operation_events_operation,priority:2"`
	TenantId    string        `gorm:"not null;size:128"`
	OperationId int64         `gorm:"not null;type:bigint;index:idx_fleet_operation_events_operation,priority:1"`
	LineId      sql.NullInt64 `gorm:"type:bigint"`
	Epoch       sql.NullInt64
	At          time.Time      `gorm:"not null"`
	Kind        string         `gorm:"not null;size:32"`
	Reason      string         `gorm:"not null;size:32;default:''"`
	Actor       sql.NullString `gorm:"size:320"`
	FromGrantee sql.NullString `gorm:"size:320"`
	ToGrantee   sql.NullString `gorm:"size:320"`

	ContractVersion sql.NullInt32
	Revision        sql.NullInt64
	Status          sql.NullString `gorm:"size:16"`
	RefEventId      sql.NullInt64  `gorm:"type:bigint"`
	Note            sql.NullString `gorm:"size:1000"`
	NoteRedactedAt  sql.NullTime
}

func (FleetOperationEvent) TableName() string { return "fleet_operation_events" }

// AuditExempt keeps the runtime row's heartbeat writes out of the operator audit stream.
func (FleetOperationRuntime) AuditExempt() bool { return true }

// AuditExempt: one line is written per device, and its transitions are its own state machine.
func (FleetOperationLine) AuditExempt() bool { return true }

// AuditExempt: the events are the operation's own append-only journal.
func (FleetOperationEvent) AuditExempt() bool { return true }

// DefaultOrder implements rdb.Sortable: newest first, with the id for a stable page.
func (FleetOperation) DefaultOrder() string {
	return "fleet_operations.created_at DESC, fleet_operations.id DESC"
}

// DefaultOrder implements rdb.Sortable: a line's ordinal is its position in the operator's input.
func (FleetOperationLine) DefaultOrder() string {
	return "fleet_operation_lines.ordinal ASC, fleet_operation_lines.id ASC"
}
