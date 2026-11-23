// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// The snapshots below are THIS migration's own shapes. They are not model.FleetOperation* and
// not core/ledger types, and they must never be replaced by either: a migration's shapes are a
// point in time, and pointing one at a live model silently rewrites an already-applied
// migration whenever that model changes, which breaks FRESH installs while every existing
// database applies cleanly and looks healthy. The mixins are inlined, the vocabulary in each
// CHECK is written out literally, and there is deliberately NO foreign key anywhere: a journal
// must not block or cascade a device delete, and a previous-release pod has to be able to
// delete a device while an operation that targeted it is in flight.
//
// Integer columns are int32 (integer) and int64 (bigint) on purpose: gorm maps a bare Go int to
// bigint, and the spec's columns are integers.
//
// 🔴 Reserved vocabulary is allowed by the CHECKs. The dispatch value DRAFT and the posting
// values DISPATCHING and POSTING_UNKNOWN are reserved for a later release. They are listed in
// the constraints so that releasing them is not a constraint swap racing old pods, and they are
// refused in code until then (core/ledger's Valid methods answer false for them).

type fleetOperationSnapshot struct {
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
	// of an older release never works an operation a newer release defined. It has NO default:
	// every insert states it (core/ledger.RequiredProtocol), and the CHECK refuses less than 1.
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

func (fleetOperationSnapshot) TableName() string { return "fleet_operations" }

type fleetOperationRuntimeSnapshot struct {
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

func (fleetOperationRuntimeSnapshot) TableName() string { return "fleet_operation_runtime" }

type fleetOperationContractSnapshot struct {
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

func (fleetOperationContractSnapshot) TableName() string { return "fleet_operation_contracts" }

type fleetOperationLineSnapshot struct {
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

func (fleetOperationLineSnapshot) TableName() string { return "fleet_operation_lines" }

type fleetOperationEventSnapshot struct {
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

func (fleetOperationEventSnapshot) TableName() string { return "fleet_operation_events" }

// NewFleetOperationsSchema creates the fleet-operation journals: the operation header, its
// runtime row, its append-only contracts, its lines and its append-only events. It is an
// APPENDED migration that creates new tables and touches no existing one, so previous-release
// pods overlapping a rolling upgrade never see it.
//
// Nothing writes these tables yet. The operation engine, the rejecting trigger that activates
// it and the served API land in later changes; this migration freezes the shape.
//
// # driver_protocol
//
// fleet_operations.driver_protocol records the lowest engine protocol that may drive an
// operation (1 for everything this release creates, stated by the creator through
// ledger.RequiredProtocol; the column has no default, so a creator that forgets is refused). It exists in the FIRST release that has
// journals because the pods that must honour it are this release's own: a later release that
// defines protocol 2 can only be kept off by pods that already filter on the column.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. Every table
// is AutoMigrate (a no-op when present) and carries its CHECKs in the CREATE TABLE statement,
// so a table is never present without them. Every extra index is IF NOT EXISTS.
//
// # Tenant purge
//
// Every table carries tenant_id, so the catalog-driven tenant purge classifies each directly.
// There is no foreign key between the tables, which also leaves the purge ordering free.
func NewFleetOperationsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261223120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(
				&fleetOperationSnapshot{},
				&fleetOperationRuntimeSnapshot{},
				&fleetOperationContractSnapshot{},
				&fleetOperationLineSnapshot{},
				&fleetOperationEventSnapshot{},
			); err != nil {
				return err
			}
			for _, ix := range fleetOperationPartialIndexes {
				if err := createPartialIndexIfNotExists(tx, ix); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(*gorm.DB) error {
			return errors.New("the fleet-operation journals cannot be rolled back: dropping them would discard what the platform did to devices and the evidence of it")
		},
	}
}

// partialIndex is a literal index definition. Table, columns and predicate are written out
// here rather than derived, because the DDL IS the snapshot.
type partialIndex struct {
	Model   any
	Table   string
	Name    string
	Unique  bool
	Columns []string
	Where   string
}

var fleetOperationPartialIndexes = []partialIndex{
	{Model: &fleetOperationSnapshot{}, Table: "fleet_operations", Name: "idx_fleet_operations_closed", Columns: []string{"tenant_id", "closed_at"}, Where: "evidence = 'CLOSED'"},
	{Model: &fleetOperationSnapshot{}, Table: "fleet_operations", Name: "idx_fleet_operations_ready_expiry", Columns: []string{"tenant_id", "expires_at"}, Where: "dispatch = 'READY'"},

	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "uix_fleet_operation_lines_target", Unique: true, Columns: []string{"operation_id", "target_id"}, Where: "target_id IS NOT NULL"},
	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "uix_fleet_operation_lines_reverses", Unique: true, Columns: []string{"reverses_line_id"}, Where: "posting = 'POSTED'"},
	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "idx_fleet_operation_lines_planned", Columns: []string{"operation_id", "next_attempt_at", "ordinal"}, Where: "posting = 'PLANNED'"},
	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "idx_fleet_operation_lines_posted", Columns: []string{"operation_id", "posted_at"}, Where: "posting = 'POSTED'"},
	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "idx_fleet_operation_lines_deadline", Columns: []string{"tenant_id", "settle_by"}, Where: "posting = 'POSTED' AND settlement IS NULL"},
	{Model: &fleetOperationLineSnapshot{}, Table: "fleet_operation_lines", Name: "idx_fleet_operation_lines_awaiting_evidence", Columns: []string{"target_id"}, Where: "posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL"},

	{Model: &fleetOperationEventSnapshot{}, Table: "fleet_operation_events", Name: "uix_fleet_operation_events_contradiction", Unique: true, Columns: []string{"line_id", "kind"}, Where: "kind = 'CONTRADICTING_EVIDENCE'"},
}

func createPartialIndexIfNotExists(tx *gorm.DB, ix partialIndex) error {
	if ix.Unique {
		// This package's own helper (a deliberate copy of the rdb one, see baseline.go).
		return createPartialUniqueIndexWhere(tx, ix.Model, ix.Name, ix.Where, ix.Columns...)
	}
	quoted := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		quoted[i] = `"` + c + `"`
	}
	return tx.Exec(fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "%s" ON "%s" (%s) WHERE %s`,
		ix.Name, ix.Table, strings.Join(quoted, ", "), ix.Where)).Error
}
