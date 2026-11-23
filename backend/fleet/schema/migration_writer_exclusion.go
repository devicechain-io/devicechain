// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// WriterExclusionID is the writer-exclusion migration's frozen ID.
const WriterExclusionID = "20261121000100"

// NewWriterExclusionSchema appends the fleet manager's first schema: the create intent of each
// fleet cluster, the one-writer slot that guards it, the append-only audit of every slot change,
// what the Observer last saw, one row per Job attempt, the last state the manager knows was
// written, the cloud operations seen for a cluster, and the recovery-import decisions.
//
// Every snapshot struct is declared INSIDE the function. A migration's shapes are a snapshot of a
// point in time; the live models are the current incarnation of the same datatypes and are
// separate types, so this migration is never silently rewritten when they change.
//
// It is individually re-runnable, because migrations run with UseTransaction:false and replay
// from the top after a failure: tables go through AutoMigrate on the snapshots, indexes use
// IF NOT EXISTS, functions use CREATE OR REPLACE, triggers are dropped and created, and CHECK and
// FOREIGN KEY constraints are added only behind a catalog probe. CHECK names carry a version so a
// later widening is a new migration (add _v2 NOT VALID, validate, drop _v1).
//
// It seeds nothing, including the manager_lease row, which the first manager creates itself.
func NewWriterExclusionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: WriterExclusionID,
		Migrate: func(tx *gorm.DB) error {
			// One row per fleet cluster: the create intent.
			type cloudIdentity struct {
				ID               uint       `gorm:"primaryKey"`
				ClusterUID       string     `gorm:"type:uuid;not null;uniqueIndex:ux_cloud_identities_cluster_uid"`
				OnboardingID     string     `gorm:"type:uuid;not null;index:ix_cloud_identities_onboarding_id"`
				ManagementMode   string     `gorm:"type:text;not null"`
				Cloud            string     `gorm:"type:text;not null"`
				ProjectID        string     `gorm:"type:text;not null"`
				Location         string     `gorm:"type:text;not null"`
				CloudName        string     `gorm:"type:text;not null"`
				OwnershipNonce   *string    `gorm:"type:text"`
				StateBackend     *string    `gorm:"type:text"`
				StateRef         *string    `gorm:"type:text"`
				DataClass        string     `gorm:"type:text;not null"`
				Lifecycle        string     `gorm:"type:text;not null;default:'INTENT'"`
				TombstonedAt     *time.Time `gorm:"type:timestamptz"`
				IntentRecordedAt time.Time  `gorm:"type:timestamptz;not null"`
				FirstSubmitAt    *time.Time `gorm:"type:timestamptz"`
				CloudResourceID  *string    `gorm:"type:text"`
				RecoveredVia     *string    `gorm:"type:text"`
				RecoveredAt      *time.Time `gorm:"type:timestamptz"`
				CreatedAt        time.Time  `gorm:"type:timestamptz;not null"`
				UpdatedAt        time.Time  `gorm:"type:timestamptz;not null"`
			}

			// One row per cluster: current exclusion state.
			type writerSlot struct {
				ClusterUID            string     `gorm:"primaryKey;type:uuid"`
				Epoch                 int64      `gorm:"not null;default:0"`
				State                 string     `gorm:"type:text;not null;default:'FREE'"`
				HolderRunUID          *string    `gorm:"type:uuid"`
				DoubtedFrom           *string    `gorm:"type:text"`
				UncertainSince        *time.Time `gorm:"type:timestamptz"`
				RefreshCutAt          *time.Time `gorm:"type:timestamptz"`
				CloudCutProof         *string    `gorm:"type:text"`
				CloudCutAt            *time.Time `gorm:"type:timestamptz"`
				StateCutMethod        *string    `gorm:"type:text"`
				StateCutAt            *time.Time `gorm:"type:timestamptz"`
				Containment           *string    `gorm:"type:text"`
				ContainmentObservedAt *time.Time `gorm:"type:timestamptz"`
				QuarantineReason      *string    `gorm:"type:text"`
				UpdatedAt             time.Time  `gorm:"type:timestamptz;not null"`
			}

			// Append-only audit of every slot change.
			type writerSlotTransition struct {
				ID                uint           `gorm:"primaryKey"`
				ClusterUID        string         `gorm:"type:uuid;not null;index:ix_writer_slot_transitions_cluster_uid"`
				Epoch             int64          `gorm:"not null"`
				RunUID            *string        `gorm:"type:uuid"`
				FromState         *string        `gorm:"type:text"`
				ToState           string         `gorm:"type:text;not null"`
				CloudCutProof     *string        `gorm:"type:text"`
				StateCutMethod    *string        `gorm:"type:text"`
				Evidence          datatypes.JSON `gorm:"type:jsonb"`
				Reason            *string        `gorm:"type:text"`
				ActorID           string         `gorm:"type:text;not null"`
				ActorIssuer       string         `gorm:"type:text;not null"`
				RequestID         *string        `gorm:"type:uuid"`
				ManagerLeaseEpoch int64          `gorm:"not null"`
				At                time.Time      `gorm:"type:timestamptz;not null"`
			}

			// What the Observer last saw. Kept off the slot row so that observation does not
			// contend with the slot lock.
			type clusterObservation struct {
				ClusterUID           string     `gorm:"primaryKey;type:uuid"`
				ObserverListedAt     *time.Time `gorm:"type:timestamptz"`
				ObserverListComplete bool       `gorm:"not null;default:false"`
				ObserverError        *string    `gorm:"type:text"`
				ClusterStatus        *string    `gorm:"type:text"`
				UpdatedAt            time.Time  `gorm:"type:timestamptz;not null"`
			}

			// One row per Job attempt.
			type run struct {
				ID                uint       `gorm:"primaryKey"`
				RunUID            string     `gorm:"type:uuid;not null;uniqueIndex:ux_runs_run_uid"`
				ClusterUID        string     `gorm:"type:uuid;not null;uniqueIndex:ux_runs_cluster_uid_epoch,priority:1"`
				Epoch             int64      `gorm:"not null;uniqueIndex:ux_runs_cluster_uid_epoch,priority:2"`
				RequestID         string     `gorm:"type:uuid;not null"`
				ActorID           string     `gorm:"type:text;not null"`
				ActorIssuer       string     `gorm:"type:text;not null"`
				Kind              string     `gorm:"type:text;not null"`
				Phase             string     `gorm:"type:text;not null"`
				JobNamespace      *string    `gorm:"type:text"`
				JobUID            *string    `gorm:"type:text"`
				PodUID            *string    `gorm:"type:text"`
				NodeName          *string    `gorm:"type:text"`
				CredentialMode    string     `gorm:"type:text;not null"`
				CloudIdentityRole string     `gorm:"type:text;not null"`
				CloudPrincipal    string     `gorm:"type:text;not null"`
				KsaTokenTTLSec    int        `gorm:"column:ksa_token_ttl_s;not null"`
				StsTokenTTLSec    int        `gorm:"column:sts_token_ttl_s;not null"`
				AccessTokenTTLSec int        `gorm:"column:access_token_ttl_s;not null"`
				StateRoleName     *string    `gorm:"type:text"`
				RunnerImageDigest *string    `gorm:"type:text"`
				TofuVersion       *string    `gorm:"type:text"`
				ProviderLockHash  *string    `gorm:"type:text"`
				ModuleDigest      *string    `gorm:"type:text"`
				ManagerLeaseEpoch int64      `gorm:"not null"`
				StartedAt         *time.Time `gorm:"type:timestamptz"`
				LastObservedAt    *time.Time `gorm:"type:timestamptz"`
				EndedAt           *time.Time `gorm:"type:timestamptz"`
			}

			// The last state the manager knows was written.
			type stateHead struct {
				ClusterUID     string    `gorm:"primaryKey;type:uuid"`
				Lineage        string    `gorm:"type:text;not null"`
				Serial         int64     `gorm:"not null"`
				RecordedRunUID *string   `gorm:"type:uuid"`
				RecordedAt     time.Time `gorm:"type:timestamptz;not null"`
			}

			// Cloud operations seen for a cluster.
			type cloudOperation struct {
				ID             uint       `gorm:"primaryKey"`
				ClusterUID     string     `gorm:"type:uuid;not null;uniqueIndex:ux_cloud_operations_cluster_uid_operation_name,priority:1"`
				RunID          *uint      `gorm:""`
				OperationName  string     `gorm:"type:text;not null;uniqueIndex:ux_cloud_operations_cluster_uid_operation_name,priority:2"`
				OperationType  *string    `gorm:"type:text"`
				TargetLink     *string    `gorm:"type:text"`
				Status         string     `gorm:"type:text;not null"`
				ErrorCode      *string    `gorm:"type:text"`
				ErrorMessage   *string    `gorm:"type:text"`
				Resolution     *string    `gorm:"type:text"`
				CloudStartTime *time.Time `gorm:"type:timestamptz"`
				CloudEndTime   *time.Time `gorm:"type:timestamptz"`
				Source         string     `gorm:"type:text;not null"`
				FirstSeenAt    time.Time  `gorm:"type:timestamptz;not null"`
				LastSeenAt     time.Time  `gorm:"type:timestamptz;not null"`
			}

			// One row per recovery-import decision.
			type importRecord struct {
				ID          uint           `gorm:"primaryKey"`
				ClusterUID  string         `gorm:"type:uuid;not null;index:ix_import_records_cluster_uid"`
				RunID       uint           `gorm:"not null"`
				Address     string         `gorm:"type:text;not null"`
				CloudID     *string        `gorm:"type:text"`
				Evidence    datatypes.JSON `gorm:"type:jsonb"`
				Decision    string         `gorm:"type:text;not null"`
				Reason      string         `gorm:"type:text;not null"`
				ActorID     string         `gorm:"type:text;not null"`
				ActorIssuer string         `gorm:"type:text;not null"`
				RequestID   string         `gorm:"type:uuid;not null"`
				DecidedAt   time.Time      `gorm:"type:timestamptz;not null"`
			}

			// manager_lease is a singleton, named so rather than by gorm's pluralisation.
			if err := tx.AutoMigrate(&managerLease{}); err != nil {
				return fmt.Errorf("fleet: manager_lease: %w", err)
			}
			if err := tx.AutoMigrate(
				&cloudIdentity{}, &writerSlot{}, &writerSlotTransition{}, &clusterObservation{},
				&run{}, &stateHead{}, &cloudOperation{}, &importRecord{},
			); err != nil {
				return fmt.Errorf("fleet: tables: %w", err)
			}
			return applyWriterExclusionDDL(tx)
		},
	}
}

// managerLease is the singleton fence between manager replicas. It is declared here, beside the
// migration and still its own snapshot (not a live model), because a table name that differs from
// gorm's pluralisation needs a TableName method, and a method cannot be declared on a type local to
// a function. The name carries no schema prefix: the connection's search_path puts it in the area's.
type managerLease struct {
	ID        int16     `gorm:"primaryKey;autoIncrement:false;type:smallint"`
	HolderID  string    `gorm:"type:text;not null"`
	Epoch     int64     `gorm:"column:lease_epoch;not null;default:0"`
	ExpiresAt time.Time `gorm:"type:timestamptz;not null"`
	UpdatedAt time.Time `gorm:"type:timestamptz;not null"`
}

// TableName names the singleton table.
func (managerLease) TableName() string { return "manager_lease" }

// check is a named CHECK constraint, added once behind a catalog probe.
type check struct{ table, name, expr string }

// foreignKey is a named FOREIGN KEY, added once behind a catalog probe.
type foreignKey struct{ table, name, column, refTable, refColumn string }

// The vocabularies are frozen into versioned CHECKs at first merge. Every consumer of a column
// that holds one treats a value it does not know as refusal.
const (
	slotStates       = `'FREE','HELD','UNCERTAIN','EXCLUDED','RECOVERING','QUARANTINED'`
	runKinds         = `'CREATE','CHANGE','UPGRADE','DRIFT_CHECK','DESTROY','RECOVER'`
	runPhases        = `'INTENT','JOB_CREATING','RUNNING','EXITED_OK','EXITED_FAILED','UNCERTAIN','EXCLUDED','RECONCILED'`
	cloudCutProofs   = `'TERMINATION','EXPIRY'`
	stateCutMethods  = `'PROCESS_TERMINATED','PG_ROLE_REVOKED','CLOUD_CREDENTIAL'`
	quarantineReason = `'NO_PROOF_OBTAINABLE','IDENTITY_MISMATCH','STATE_HEAD_MISMATCH','FOREIGN_LOCK_HOLDER','UNSETTLED_ERRORED_OPERATION','SECOND_POD_FOR_RUN','CREATE_TIME_BEFORE_INTENT'`
	importReasons    = `'VERIFIED','NAME_MISMATCH','PROJECT_MISMATCH','LOCATION_MISMATCH','NONCE_MISMATCH','CREATE_TIME_BEFORE_INTENT','IDENTITY_MISMATCH','POOL_PARENT_NOT_VERIFIED','POOL_NOT_DECLARED'`
)

func writerExclusionChecks() []check {
	return []check{
		{"manager_lease", "ck_manager_lease_singleton_v1", `id = 1`},

		{"cloud_identities", "ck_cloud_identities_management_mode_v1", `management_mode IN ('managed','observe_only')`},
		{"cloud_identities", "ck_cloud_identities_cloud_v1", `cloud IN ('gke')`},
		{"cloud_identities", "ck_cloud_identities_state_backend_v1", `state_backend IS NULL OR state_backend IN ('gcs','pg')`},
		{"cloud_identities", "ck_cloud_identities_data_class_v1", `data_class IN ('disposable','retained')`},
		{"cloud_identities", "ck_cloud_identities_lifecycle_v1", `lifecycle IN ('INTENT','ACTIVE','TEARING_DOWN','DESTROYED')`},
		{"cloud_identities", "ck_cloud_identities_recovered_via_v1", `recovered_via IS NULL OR recovered_via IN ('apply','import')`},
		// 26 lowercase base32 characters: a 128-bit nonce.
		{"cloud_identities", "ck_cloud_identities_nonce_shape_v1", `ownership_nonce IS NULL OR ownership_nonce ~ '^[a-z2-7]{26}$'`},
		// managed implies nonce, backend and state ref; observe_only implies none of them.
		{"cloud_identities", "ck_cloud_identities_mode_columns_v1",
			`(management_mode = 'managed' AND ownership_nonce IS NOT NULL AND state_backend IS NOT NULL AND state_ref IS NOT NULL)` +
				` OR (management_mode = 'observe_only' AND ownership_nonce IS NULL AND state_backend IS NULL AND state_ref IS NULL)`},
		{"cloud_identities", "ck_cloud_identities_recovery_pair_v1", `(cloud_resource_id IS NULL) = (recovered_via IS NULL)`},
		// A set resource id is a real one: the empty string would satisfy the pair rule and the
		// immutability trigger while naming nothing.
		{"cloud_identities", "ck_cloud_identities_cloud_resource_id_nonempty_v1", `cloud_resource_id IS NULL OR cloud_resource_id <> ''`},

		{"writer_slots", "ck_writer_slots_state_v1", `state IN (` + slotStates + `)`},
		{"writer_slots", "ck_writer_slots_doubted_from_v1", `doubted_from IS NULL OR doubted_from IN ('HELD','RECOVERING')`},
		{"writer_slots", "ck_writer_slots_cloud_cut_proof_v1", `cloud_cut_proof IS NULL OR cloud_cut_proof IN (` + cloudCutProofs + `)`},
		{"writer_slots", "ck_writer_slots_state_cut_method_v1", `state_cut_method IS NULL OR state_cut_method IN (` + stateCutMethods + `)`},
		{"writer_slots", "ck_writer_slots_containment_v1", `containment IS NULL OR containment = 'IDENTITY_DISABLED'`},
		{"writer_slots", "ck_writer_slots_quarantine_reason_v1", `quarantine_reason IS NULL OR quarantine_reason IN (` + quarantineReason + `)`},
		// A quarantined slot says why; the reason is the recovery operator's starting point.
		{"writer_slots", "ck_writer_slots_quarantine_has_reason_v1", `state <> 'QUARANTINED' OR quarantine_reason IS NOT NULL`},
		// FREE implies no holder and no proof columns.
		{"writer_slots", "ck_writer_slots_free_is_clear_v1",
			`state <> 'FREE' OR (holder_run_uid IS NULL AND cloud_cut_proof IS NULL AND cloud_cut_at IS NULL AND state_cut_method IS NULL AND state_cut_at IS NULL AND refresh_cut_at IS NULL)`},
		// HELD, UNCERTAIN and RECOVERING imply a holder.
		{"writer_slots", "ck_writer_slots_holder_v1", `state NOT IN ('HELD','UNCERTAIN','RECOVERING') OR holder_run_uid IS NOT NULL`},
		{"writer_slots", "ck_writer_slots_uncertain_v1", `state <> 'UNCERTAIN' OR doubted_from IS NOT NULL`},
		// EXCLUDED implies BOTH cuts, each with its time. There is no waiver.
		{"writer_slots", "ck_writer_slots_excluded_both_cuts_v1",
			`state <> 'EXCLUDED' OR (cloud_cut_proof IS NOT NULL AND cloud_cut_at IS NOT NULL AND state_cut_method IS NOT NULL AND state_cut_at IS NOT NULL)`},
		{"writer_slots", "ck_writer_slots_expiry_v1",
			`cloud_cut_proof IS DISTINCT FROM 'EXPIRY' OR (refresh_cut_at IS NOT NULL AND state_cut_method IS DISTINCT FROM 'PROCESS_TERMINATED')`},
		{"writer_slots", "ck_writer_slots_termination_v1",
			`cloud_cut_proof IS DISTINCT FROM 'TERMINATION' OR state_cut_method = 'PROCESS_TERMINATED'`},

		{"writer_slot_transitions", "ck_writer_slot_transitions_from_state_v1", `from_state IS NULL OR from_state IN (` + slotStates + `)`},
		{"writer_slot_transitions", "ck_writer_slot_transitions_to_state_v1", `to_state IN (` + slotStates + `)`},
		{"writer_slot_transitions", "ck_writer_slot_transitions_proof_v1", `cloud_cut_proof IS NULL OR cloud_cut_proof IN (` + cloudCutProofs + `)`},
		{"writer_slot_transitions", "ck_writer_slot_transitions_state_cut_v1", `state_cut_method IS NULL OR state_cut_method IN (` + stateCutMethods + `)`},

		{"runs", "ck_runs_kind_v1", `kind IN (` + runKinds + `)`},
		{"runs", "ck_runs_phase_v1", `phase IN (` + runPhases + `)`},
		{"runs", "ck_runs_credential_mode_v1", `credential_mode IN ('kind_jwks','gke_wi')`},
		{"runs", "ck_runs_cloud_identity_role_v1", `cloud_identity_role IN ('apply','destroy')`},
		// The residual a run's EXPIRY proof waits out is the sum of these; a zero lifetime
		// would make that sum meaningless, so each must be positive.
		{"runs", "ck_runs_token_ttls_positive_v1", `ksa_token_ttl_s > 0 AND sts_token_ttl_s > 0 AND access_token_ttl_s > 0`},

		{"cloud_operations", "ck_cloud_operations_status_v1", `status IN ('PENDING','RUNNING','DONE','ABORTING','UNKNOWN')`},
		{"cloud_operations", "ck_cloud_operations_resolution_v1", `resolution IS NULL OR resolution IN ('RECONCILED','QUARANTINED')`},
		{"cloud_operations", "ck_cloud_operations_source_v1", `source IN ('RUNNER_LOG','OBSERVER_LIST')`},

		{"import_records", "ck_import_records_decision_v1", `decision IN ('IMPORTED','REFUSED')`},
		{"import_records", "ck_import_records_reason_v1", `reason IN (` + importReasons + `)`},
		// An import was verified; a refusal names what failed.
		{"import_records", "ck_import_records_verdict_matches_reason_v1",
			`(decision = 'IMPORTED' AND reason = 'VERIFIED') OR (decision = 'REFUSED' AND reason <> 'VERIFIED')`},
	}
}

func writerExclusionForeignKeys() []foreignKey {
	return []foreignKey{
		{"writer_slots", "fk_writer_slots_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"writer_slot_transitions", "fk_writer_slot_transitions_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"cluster_observations", "fk_cluster_observations_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"runs", "fk_runs_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"state_heads", "fk_state_heads_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"cloud_operations", "fk_cloud_operations_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"cloud_operations", "fk_cloud_operations_run_id", "run_id", "runs", "id"},
		{"import_records", "fk_import_records_cluster_uid", "cluster_uid", "cloud_identities", "cluster_uid"},
		{"import_records", "fk_import_records_run_id", "run_id", "runs", "id"},
		// writer_slots.holder_run_uid has NO foreign key: it would be circular with runs.
		// Its integrity is the transaction that sets it, and a test.
	}
}

// Statements that are individually re-runnable on their own: IF NOT EXISTS, CREATE OR REPLACE,
// or DROP IF EXISTS followed by CREATE.
// UnsettledPredicateV1 is the SQL text of "this cloud operation is not settled": settled is DONE
// with no error, or with a recorded resolution. It is the predicate of ix_cloud_operations_unsettled
// AND of every query that counts unsettled operations, so the planner can use the partial index
// (a query's predicate must imply the index's). It is frozen with this migration: changing the
// meaning needs a new migration and a V2 name, never an edit here.
const UnsettledPredicateV1 = `NOT (status = 'DONE' AND (error_code IS NULL OR resolution IN ('RECONCILED', 'QUARANTINED')))`

var writerExclusionStatements = []string{
	// One live identity per cloud cluster name; a destroyed record frees its name.
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_cloud_identities_ownership_nonce ON cloud_identities (ownership_nonce) WHERE ownership_nonce IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_cloud_identities_live_cloud_name ON cloud_identities (cloud, project_id, location, cloud_name) WHERE lifecycle <> 'DESTROYED'`,
	// A second recovery import for one address is refused.
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_import_records_imported_address ON import_records (cluster_uid, address) WHERE decision = 'IMPORTED'`,
	// The Observer and the admission check read the unsettled operations of one cluster. Settled
	// means DONE and (no error, or a recorded resolution). The text is shared with the admission
	// and finish-recovery queries so the planner can use this partial index for them.
	`CREATE INDEX IF NOT EXISTS ix_cloud_operations_unsettled ON cloud_operations (cluster_uid) WHERE ` + UnsettledPredicateV1,

	// Identity is immutable once set: a later TTL or teardown design must start from a fact
	// that cannot be flipped.
	`CREATE OR REPLACE FUNCTION cloud_identities_immutable() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
	IF NEW.cluster_uid IS DISTINCT FROM OLD.cluster_uid THEN
		RAISE EXCEPTION 'cloud_identities.cluster_uid is immutable' USING ERRCODE = 'integrity_constraint_violation';
	END IF;
	IF NEW.management_mode IS DISTINCT FROM OLD.management_mode THEN
		RAISE EXCEPTION 'cloud_identities.management_mode is immutable' USING ERRCODE = 'integrity_constraint_violation';
	END IF;
	IF NEW.data_class IS DISTINCT FROM OLD.data_class THEN
		RAISE EXCEPTION 'cloud_identities.data_class is immutable' USING ERRCODE = 'integrity_constraint_violation';
	END IF;
	IF OLD.ownership_nonce IS NOT NULL AND NEW.ownership_nonce IS DISTINCT FROM OLD.ownership_nonce THEN
		RAISE EXCEPTION 'cloud_identities.ownership_nonce is immutable once set' USING ERRCODE = 'integrity_constraint_violation';
	END IF;
	IF OLD.cloud_resource_id IS NOT NULL AND NEW.cloud_resource_id IS DISTINCT FROM OLD.cloud_resource_id THEN
		RAISE EXCEPTION 'cloud_identities.cloud_resource_id is immutable once set' USING ERRCODE = 'integrity_constraint_violation';
	END IF;
	RETURN NEW;
END
$fn$`,
	`DROP TRIGGER IF EXISTS tr_cloud_identities_immutable ON cloud_identities`,
	`CREATE TRIGGER tr_cloud_identities_immutable BEFORE UPDATE ON cloud_identities FOR EACH ROW EXECUTE FUNCTION cloud_identities_immutable()`,

	// The audit trail is never updated or deleted.
	`CREATE OR REPLACE FUNCTION writer_slot_transitions_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
	RAISE EXCEPTION 'writer_slot_transitions is append-only' USING ERRCODE = 'integrity_constraint_violation';
END
$fn$`,
	`DROP TRIGGER IF EXISTS tr_writer_slot_transitions_append_only ON writer_slot_transitions`,
	`CREATE TRIGGER tr_writer_slot_transitions_append_only BEFORE UPDATE OR DELETE ON writer_slot_transitions FOR EACH ROW EXECUTE FUNCTION writer_slot_transitions_append_only()`,
	`DROP TRIGGER IF EXISTS tr_writer_slot_transitions_no_truncate ON writer_slot_transitions`,
	`CREATE TRIGGER tr_writer_slot_transitions_no_truncate BEFORE TRUNCATE ON writer_slot_transitions FOR EACH STATEMENT EXECUTE FUNCTION writer_slot_transitions_append_only()`,
}

func applyWriterExclusionDDL(tx *gorm.DB) error {
	for _, fk := range writerExclusionForeignKeys() {
		present, err := constraintExists(tx, fk.table, fk.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		stmt := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)`,
			fk.table, fk.name, fk.column, fk.refTable, fk.refColumn)
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("fleet: %s: %w", fk.name, err)
		}
	}
	for _, c := range writerExclusionChecks() {
		present, err := constraintExists(tx, c.table, c.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)`, c.table, c.name, c.expr)).Error; err != nil {
			return fmt.Errorf("fleet: %s: %w", c.name, err)
		}
	}
	for _, s := range writerExclusionStatements {
		if err := tx.Exec(s).Error; err != nil {
			return fmt.Errorf("fleet: %q: %w", firstLine(s), err)
		}
	}
	return nil
}

// constraintExists probes pg_constraint in the schema the connection is pinned to. Postgres has
// no ADD CONSTRAINT IF NOT EXISTS, and a migration that replays from the top after a failure must
// not fail on the constraint it already added.
func constraintExists(tx *gorm.DB, table, name string) (bool, error) {
	var n int
	err := tx.Raw(`SELECT count(*) FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = t.relnamespace
		WHERE ns.nspname = current_schema() AND t.relname = ? AND c.conname = ?`, table, name).Scan(&n).Error
	if err != nil {
		return false, fmt.Errorf("fleet: probing constraint %s: %w", name, err)
	}
	return n > 0, nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
