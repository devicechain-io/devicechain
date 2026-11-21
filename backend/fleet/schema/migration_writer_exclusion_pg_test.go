// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-fleet/internal/pgtest"
)

const (
	clusterA = "11111111-1111-1111-1111-111111111111"
	clusterB = "22222222-2222-2222-2222-222222222222"
	runUID   = "33333333-3333-3333-3333-333333333333"
	reqUID   = "44444444-4444-4444-4444-444444444444"
	nonceA   = "abcdefghijklmnopqrstuvwxyz"
	nonceB   = "bbcdefghijklmnopqrstuvwxyz"
)

// migrated returns a fresh database holding the fleet schema, connected the way a service's own
// connection is (search_path pinned, table prefix set).
func migrated(t *testing.T) *gorm.DB {
	t.Helper()
	env := pgtest.OpenArea(t, "fleet")
	require.NoError(t, NewWriterExclusionSchema().Migrate(env.Gorm))
	return env.Gorm
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	require.NoError(t, db.Exec(sql, args...).Error, sql)
}

// refuse asserts the statement fails AND names what refused it. Naming matters: a row that
// violates two constraints reports only one, so a bare "it failed" proves nothing about which
// guard did the work.
func refuse(t *testing.T, db *gorm.DB, want string, sql string, args ...any) {
	t.Helper()
	err := db.Exec(sql, args...).Error
	require.Error(t, err, "expected %s to refuse: %s", want, sql)
	require.Contains(t, err.Error(), want, sql)
}

// identity inserts a valid managed cluster identity, with overrides on the columns named.
func identity(t *testing.T, db *gorm.DB, uid, nonce string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
		ownership_nonce, state_backend, state_ref, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
		VALUES (?, ?, 'managed', 'gke', 'example-project', 'us-central1-a', ?, ?, 'gcs', 'bucket/prefix', 'retained', 'INTENT', now(), now(), now())`,
		uid, reqUID, "dcf-"+uid[:8], nonce)
}

func slot(t *testing.T, db *gorm.DB, uid string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO writer_slots (cluster_uid, epoch, state, updated_at) VALUES (?, 0, 'FREE', now())`, uid)
}

func run(t *testing.T, db *gorm.DB, uid string, epoch int) uint {
	t.Helper()
	var id uint
	require.NoError(t, db.Raw(`INSERT INTO runs (run_uid, cluster_uid, epoch, request_id, actor_id, actor_issuer, kind, phase, credential_mode,
		cloud_identity_role, cloud_principal, ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, manager_lease_epoch)
		VALUES (gen_random_uuid(), ?, ?, ?, 'a', 'fleet-manager', 'CREATE', 'INTENT', 'kind_jwks', 'apply', 'sa@example', 3600, 3600, 3600, 1) RETURNING id`,
		uid, epoch, reqUID).Scan(&id).Error)
	return id
}

func TestFleetPGMigrationAppliesAndReapplies(t *testing.T) {
	db := migrated(t)
	// Replay from the top after a failure: every statement must be individually re-runnable.
	require.NoError(t, NewWriterExclusionSchema().Migrate(db))
	require.NoError(t, NewWriterExclusionSchema().Migrate(db))

	// The migration seeds nothing, including the singleton lease.
	for _, table := range []string{"manager_lease", "cloud_identities", "writer_slots", "writer_slot_transitions", "cluster_observations", "runs", "state_heads", "cloud_operations", "import_records"} {
		var n int
		require.NoError(t, db.Raw(`SELECT count(*) FROM `+table).Scan(&n).Error, table)
		require.Zero(t, n, "%s must be empty after the migration", table)
	}
}

// A replay that stopped part way resumes: a dropped constraint, trigger and index come back.
func TestFleetPGReplayAfterPartialFailureRestoresWhatIsMissing(t *testing.T) {
	db := migrated(t)
	mustExec(t, db, `ALTER TABLE writer_slots DROP CONSTRAINT ck_writer_slots_state_v1`)
	mustExec(t, db, `DROP TRIGGER tr_cloud_identities_immutable ON cloud_identities`)
	mustExec(t, db, `DROP INDEX ux_cloud_identities_ownership_nonce`)
	mustExec(t, db, `ALTER TABLE runs DROP CONSTRAINT fk_runs_cluster_uid`)
	require.NoError(t, NewWriterExclusionSchema().Migrate(db))

	identity(t, db, clusterA, nonceA)
	slot(t, db, clusterA)
	refuse(t, db, "ck_writer_slots_state_v1", `UPDATE writer_slots SET state = 'BOGUS'`)
	refuse(t, db, "immutable", `UPDATE cloud_identities SET data_class = 'disposable'`)
	refuse(t, db, "ux_cloud_identities_ownership_nonce", `INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
		ownership_nonce, state_backend, state_ref, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
		VALUES (?, ?, 'managed', 'gke', 'p', 'l', 'other', ?, 'gcs', 'r', 'retained', 'INTENT', now(), now(), now())`, clusterB, reqUID, nonceA)
	refuse(t, db, "fk_runs_cluster_uid", `INSERT INTO runs (run_uid, cluster_uid, epoch, request_id, actor_id, actor_issuer, kind, phase, credential_mode,
		cloud_identity_role, cloud_principal, ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, manager_lease_epoch)
		VALUES (gen_random_uuid(), ?, 1, ?, 'a', 'i', 'CREATE', 'INTENT', 'kind_jwks', 'apply', 'p', 1, 1, 1, 1)`, clusterB, reqUID)
}

func TestFleetPGEveryCheckRefusesItsIllegalRow(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)
	slot(t, db, clusterA)
	runID := run(t, db, clusterA, 1)

	// An identity insert that violates exactly one column rule.
	badIdentity := func(set string, args ...any) (string, []any) {
		cols := map[string]string{
			"management_mode": "'managed'", "cloud": "'gke'", "state_backend": "'gcs'", "data_class": "'retained'",
			"lifecycle": "'INTENT'", "recovered_via": "NULL", "cloud_resource_id": "NULL", "ownership_nonce": "'" + nonceB + "'",
			"state_ref": "'r'",
		}
		for _, kv := range strings.Split(set, ";") {
			p := strings.SplitN(kv, "=", 2)
			cols[p[0]] = p[1]
		}
		return `INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
			ownership_nonce, state_backend, state_ref, data_class, lifecycle, recovered_via, cloud_resource_id, intent_recorded_at, created_at, updated_at)
			VALUES ('` + clusterB + `', '` + reqUID + `', ` + cols["management_mode"] + `, ` + cols["cloud"] + `, 'p', 'l', 'n', ` + cols["ownership_nonce"] + `, ` +
			cols["state_backend"] + `, ` + cols["state_ref"] + `, ` + cols["data_class"] + `, ` + cols["lifecycle"] + `, ` + cols["recovered_via"] + `, ` +
			cols["cloud_resource_id"] + `, now(), now(), now())`, args
	}
	idCase := func(name, set string) {
		sql, args := badIdentity(set)
		refuse(t, db, name, sql, args...)
	}

	cases := map[string]func(){
		"ck_manager_lease_singleton_v1": func() {
			refuse(t, db, "ck_manager_lease_singleton_v1", `INSERT INTO manager_lease (id, holder_id, lease_epoch, expires_at, updated_at) VALUES (2, 'h', 0, now(), now())`)
		},
		"ck_cloud_identities_management_mode_v1": func() { idCase("ck_cloud_identities_management_mode_v1", "management_mode='x'") },
		"ck_cloud_identities_cloud_v1":           func() { idCase("ck_cloud_identities_cloud_v1", "cloud='aws'") },
		"ck_cloud_identities_state_backend_v1":   func() { idCase("ck_cloud_identities_state_backend_v1", "state_backend='s3'") },
		"ck_cloud_identities_data_class_v1":      func() { idCase("ck_cloud_identities_data_class_v1", "data_class='x'") },
		"ck_cloud_identities_lifecycle_v1":       func() { idCase("ck_cloud_identities_lifecycle_v1", "lifecycle='x'") },
		"ck_cloud_identities_recovered_via_v1": func() {
			idCase("ck_cloud_identities_recovered_via_v1", "recovered_via='x';cloud_resource_id='r'")
		},
		"ck_cloud_identities_nonce_shape_v1": func() { idCase("ck_cloud_identities_nonce_shape_v1", "ownership_nonce='UPPERCASE'") },
		"ck_cloud_identities_mode_columns_v1": func() {
			idCase("ck_cloud_identities_mode_columns_v1", "ownership_nonce=NULL")
			idCase("ck_cloud_identities_mode_columns_v1", "state_backend=NULL")
			idCase("ck_cloud_identities_mode_columns_v1", "state_ref=NULL")
			idCase("ck_cloud_identities_mode_columns_v1", "management_mode='observe_only'") // still has a nonce
		},
		"ck_cloud_identities_recovery_pair_v1": func() {
			idCase("ck_cloud_identities_recovery_pair_v1", "cloud_resource_id='r'")
			idCase("ck_cloud_identities_recovery_pair_v1", "recovered_via='apply'")
		},
	}

	setSlot := func(name, set string) {
		refuse(t, db, name, `UPDATE writer_slots SET `+set+` WHERE cluster_uid = '`+clusterA+`'`)
	}
	held := `state = 'HELD', holder_run_uid = '` + runUID + `'`
	cases["ck_writer_slots_state_v1"] = func() { setSlot("ck_writer_slots_state_v1", "state = 'BOGUS'") }
	cases["ck_writer_slots_doubted_from_v1"] = func() { setSlot("ck_writer_slots_doubted_from_v1", "doubted_from = 'FREE'") }
	cases["ck_writer_slots_cloud_cut_proof_v1"] = func() { setSlot("ck_writer_slots_cloud_cut_proof_v1", held+", cloud_cut_proof = 'WAIVER'") }
	cases["ck_writer_slots_state_cut_method_v1"] = func() { setSlot("ck_writer_slots_state_cut_method_v1", held+", state_cut_method = 'NONE'") }
	cases["ck_writer_slots_containment_v1"] = func() { setSlot("ck_writer_slots_containment_v1", held+", containment = 'PROOF'") }
	cases["ck_writer_slots_quarantine_reason_v1"] = func() { setSlot("ck_writer_slots_quarantine_reason_v1", held+", quarantine_reason = 'because'") }
	cases["ck_writer_slots_quarantine_has_reason_v1"] = func() { setSlot("ck_writer_slots_quarantine_has_reason_v1", "state = 'QUARANTINED'") }
	cases["ck_writer_slots_free_is_clear_v1"] = func() {
		setSlot("ck_writer_slots_free_is_clear_v1", "holder_run_uid = '"+runUID+"'")
		setSlot("ck_writer_slots_free_is_clear_v1", "refresh_cut_at = now()")
		setSlot("ck_writer_slots_free_is_clear_v1", "cloud_cut_at = now()")
	}
	cases["ck_writer_slots_holder_v1"] = func() {
		for _, s := range []string{"HELD", "UNCERTAIN", "RECOVERING"} {
			setSlot("ck_writer_slots_holder_v1", "state = '"+s+"', doubted_from = 'HELD'")
		}
	}
	cases["ck_writer_slots_uncertain_v1"] = func() { setSlot("ck_writer_slots_uncertain_v1", "state = 'UNCERTAIN', holder_run_uid = '"+runUID+"'") }
	cases["ck_writer_slots_excluded_both_cuts_v1"] = func() {
		setSlot("ck_writer_slots_excluded_both_cuts_v1", "state = 'EXCLUDED'")
		// The cloud cut alone, and the state cut alone, are each not enough.
		setSlot("ck_writer_slots_excluded_both_cuts_v1", "state = 'EXCLUDED', cloud_cut_proof = 'TERMINATION', cloud_cut_at = now(), state_cut_method = 'PROCESS_TERMINATED'")
		setSlot("ck_writer_slots_excluded_both_cuts_v1", "state = 'EXCLUDED', state_cut_method = 'PG_ROLE_REVOKED', state_cut_at = now()")
	}
	cases["ck_writer_slots_expiry_v1"] = func() {
		setSlot("ck_writer_slots_expiry_v1", held+", cloud_cut_proof = 'EXPIRY', state_cut_method = 'CLOUD_CREDENTIAL'") // no refresh cut
		setSlot("ck_writer_slots_expiry_v1", held+", cloud_cut_proof = 'EXPIRY', refresh_cut_at = now(), state_cut_method = 'PROCESS_TERMINATED'")
	}
	cases["ck_writer_slots_termination_v1"] = func() {
		setSlot("ck_writer_slots_termination_v1", held+", cloud_cut_proof = 'TERMINATION', state_cut_method = 'PG_ROLE_REVOKED'")
	}

	trans := func(name, col, val string) {
		cols := map[string]string{"from_state": "'FREE'", "to_state": "'HELD'", "cloud_cut_proof": "NULL", "state_cut_method": "NULL"}
		cols[col] = val
		refuse(t, db, name, `INSERT INTO writer_slot_transitions (cluster_uid, epoch, from_state, to_state, cloud_cut_proof, state_cut_method, actor_id, actor_issuer, manager_lease_epoch, at)
			VALUES ('`+clusterA+`', 1, `+cols["from_state"]+`, `+cols["to_state"]+`, `+cols["cloud_cut_proof"]+`, `+cols["state_cut_method"]+`, 'a', 'fleet-manager', 1, now())`)
	}
	cases["ck_writer_slot_transitions_from_state_v1"] = func() { trans("ck_writer_slot_transitions_from_state_v1", "from_state", "'BOGUS'") }
	cases["ck_writer_slot_transitions_to_state_v1"] = func() { trans("ck_writer_slot_transitions_to_state_v1", "to_state", "'BOGUS'") }
	cases["ck_writer_slot_transitions_proof_v1"] = func() { trans("ck_writer_slot_transitions_proof_v1", "cloud_cut_proof", "'WAIVER'") }
	cases["ck_writer_slot_transitions_state_cut_v1"] = func() { trans("ck_writer_slot_transitions_state_cut_v1", "state_cut_method", "'NONE'") }

	runCase := func(name, col, val string) {
		cols := map[string]string{"kind": "'CREATE'", "phase": "'INTENT'", "credential_mode": "'kind_jwks'", "cloud_identity_role": "'apply'",
			"ksa": "3600", "sts": "3600", "access": "3600"}
		cols[col] = val
		refuse(t, db, name, `INSERT INTO runs (run_uid, cluster_uid, epoch, request_id, actor_id, actor_issuer, kind, phase, credential_mode,
			cloud_identity_role, cloud_principal, ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, manager_lease_epoch)
			VALUES (gen_random_uuid(), '`+clusterA+`', 99, '`+reqUID+`', 'a', 'i', `+cols["kind"]+`, `+cols["phase"]+`, `+cols["credential_mode"]+`, `+cols["cloud_identity_role"]+`,
			'p', `+cols["ksa"]+`, `+cols["sts"]+`, `+cols["access"]+`, 1)`)
	}
	cases["ck_runs_kind_v1"] = func() { runCase("ck_runs_kind_v1", "kind", "'DELETE_EVERYTHING'") }
	cases["ck_runs_phase_v1"] = func() { runCase("ck_runs_phase_v1", "phase", "'MYSTERY'") }
	cases["ck_runs_credential_mode_v1"] = func() { runCase("ck_runs_credential_mode_v1", "credential_mode", "'key_file'") }
	cases["ck_runs_cloud_identity_role_v1"] = func() { runCase("ck_runs_cloud_identity_role_v1", "cloud_identity_role", "'root'") }
	cases["ck_runs_token_ttls_positive_v1"] = func() {
		runCase("ck_runs_token_ttls_positive_v1", "ksa", "0")
		runCase("ck_runs_token_ttls_positive_v1", "sts", "0")
		runCase("ck_runs_token_ttls_positive_v1", "access", "-1")
	}

	opCase := func(name, col, val string) {
		cols := map[string]string{"status": "'DONE'", "resolution": "NULL", "source": "'RUNNER_LOG'"}
		cols[col] = val
		refuse(t, db, name, `INSERT INTO cloud_operations (cluster_uid, operation_name, status, resolution, source, first_seen_at, last_seen_at)
			VALUES ('`+clusterA+`', 'op-'||gen_random_uuid(), `+cols["status"]+`, `+cols["resolution"]+`, `+cols["source"]+`, now(), now())`)
	}
	cases["ck_cloud_operations_status_v1"] = func() { opCase("ck_cloud_operations_status_v1", "status", "'FINISHED'") }
	cases["ck_cloud_operations_resolution_v1"] = func() { opCase("ck_cloud_operations_resolution_v1", "resolution", "'WAIVED'") }
	cases["ck_cloud_operations_source_v1"] = func() { opCase("ck_cloud_operations_source_v1", "source", "'GUESS'") }

	impCase := func(name, decision, reason string) {
		refuse(t, db, name, `INSERT INTO import_records (cluster_uid, run_id, address, decision, reason, actor_id, actor_issuer, request_id, decided_at)
			VALUES ('`+clusterA+`', `+itoa(runID)+`, 'google_container_cluster.this', '`+decision+`', '`+reason+`', 'a', 'i', '`+reqUID+`', now())`)
	}
	cases["ck_import_records_decision_v1"] = func() { impCase("ck_import_records_decision_v1", "ADOPTED", "VERIFIED") }
	cases["ck_import_records_reason_v1"] = func() { impCase("ck_import_records_reason_v1", "REFUSED", "BECAUSE") }
	cases["ck_import_records_verdict_matches_reason_v1"] = func() {
		impCase("ck_import_records_verdict_matches_reason_v1", "IMPORTED", "NAME_MISMATCH")
		impCase("ck_import_records_verdict_matches_reason_v1", "REFUSED", "VERIFIED")
	}

	// Every CHECK the migration declares has a case. Adding one without one fails here.
	for _, c := range writerExclusionChecks() {
		fn, ok := cases[c.name]
		require.True(t, ok, "CHECK %s has no illegal-row case", c.name)
		fn()
		delete(cases, c.name)
	}
	require.Empty(t, cases, "cases name constraints the migration does not declare")
}

func itoa(u uint) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
		if u == 0 {
			break
		}
	}
	return string(b[i:])
}

// The legal rows the checks must NOT refuse: a schema that refuses everything is an outage.
func TestFleetPGLegalRowsAreAccepted(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)
	slot(t, db, clusterA)
	runID := run(t, db, clusterA, 1)
	_ = runID

	// An observe-only record has none of nonce, backend and state ref.
	mustExec(t, db, `INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
		data_class, lifecycle, intent_recorded_at, created_at, updated_at)
		VALUES (?, ?, 'observe_only', 'gke', 'example-project', 'us-central1-a', 'adopted', 'disposable', 'ACTIVE', now(), now(), now())`, clusterB, reqUID)

	// The slot's whole life: HELD, UNCERTAIN, EXCLUDED with both cuts, FREE again.
	mustExec(t, db, `UPDATE writer_slots SET state='HELD', holder_run_uid=?, epoch=1 WHERE cluster_uid=?`, runUID, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='UNCERTAIN', doubted_from='HELD', uncertain_since=now() WHERE cluster_uid=?`, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='EXCLUDED', refresh_cut_at=now(), cloud_cut_proof='EXPIRY', cloud_cut_at=now(), state_cut_method='PG_ROLE_REVOKED', state_cut_at=now() WHERE cluster_uid=?`, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='QUARANTINED', quarantine_reason='NO_PROOF_OBTAINABLE', cloud_cut_proof=NULL, cloud_cut_at=NULL, state_cut_method=NULL, state_cut_at=NULL, refresh_cut_at=NULL WHERE cluster_uid=?`, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='EXCLUDED', quarantine_reason=NULL, refresh_cut_at=now(), cloud_cut_proof='TERMINATION', cloud_cut_at=now(), state_cut_method='PROCESS_TERMINATED', state_cut_at=now() WHERE cluster_uid=?`, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='FREE', holder_run_uid=NULL, doubted_from=NULL, uncertain_since=NULL, refresh_cut_at=NULL, cloud_cut_proof=NULL, cloud_cut_at=NULL, state_cut_method=NULL, state_cut_at=NULL WHERE cluster_uid=?`, clusterA)
}

func TestFleetPGIdentityIsImmutable(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)

	refuse(t, db, "cluster_uid is immutable", `UPDATE cloud_identities SET cluster_uid = ?`, clusterB)
	refuse(t, db, "management_mode is immutable", `UPDATE cloud_identities SET management_mode = 'observe_only', ownership_nonce = NULL, state_backend = NULL, state_ref = NULL`)
	refuse(t, db, "data_class is immutable", `UPDATE cloud_identities SET data_class = 'disposable'`)
	refuse(t, db, "ownership_nonce is immutable once set", `UPDATE cloud_identities SET ownership_nonce = ?`, nonceB)
	refuse(t, db, "ownership_nonce is immutable once set", `UPDATE cloud_identities SET ownership_nonce = NULL, management_mode = 'managed'`)

	// cloud_resource_id (with recovered_via) is settable ONCE, then frozen.
	mustExec(t, db, `UPDATE cloud_identities SET cloud_resource_id = 'id-1', recovered_via = 'apply', recovered_at = now()`)
	refuse(t, db, "cloud_resource_id is immutable once set", `UPDATE cloud_identities SET cloud_resource_id = 'id-2'`)

	// What is mutable stays mutable: lifecycle, first_submit_at, tombstone.
	mustExec(t, db, `UPDATE cloud_identities SET lifecycle = 'ACTIVE', first_submit_at = now(), tombstoned_at = now(), updated_at = now()`)
}

func TestFleetPGTransitionsAreAppendOnly(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)
	mustExec(t, db, `INSERT INTO writer_slot_transitions (cluster_uid, epoch, from_state, to_state, actor_id, actor_issuer, manager_lease_epoch, at)
		VALUES (?, 1, 'FREE', 'HELD', 'a', 'fleet-manager', 1, now())`, clusterA)
	refuse(t, db, "append-only", `UPDATE writer_slot_transitions SET reason = 'edited'`)
	refuse(t, db, "append-only", `DELETE FROM writer_slot_transitions`)
	refuse(t, db, "append-only", `TRUNCATE writer_slot_transitions`)
	var n int
	require.NoError(t, db.Raw(`SELECT count(*) FROM writer_slot_transitions`).Scan(&n).Error)
	require.Equal(t, 1, n)
}

func TestFleetPGUniqueIndexes(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)
	insert := func(uid, nonce, name, lifecycle string) error {
		return db.Exec(`INSERT INTO cloud_identities (cluster_uid, onboarding_id, management_mode, cloud, project_id, location, cloud_name,
			ownership_nonce, state_backend, state_ref, data_class, lifecycle, intent_recorded_at, created_at, updated_at)
			VALUES (?, ?, 'managed', 'gke', 'example-project', 'us-central1-a', ?, ?, 'gcs', 'r', 'retained', ?, now(), now(), now())`,
			uid, reqUID, name, nonce, lifecycle).Error
	}
	// The name identity() gave clusterA.
	taken := "dcf-" + clusterA[:8]
	require.Error(t, insert(clusterB, nonceB, taken, "INTENT"), "two live identities for one cloud cluster name")
	// A destroyed record frees its name.
	mustExec(t, db, `UPDATE cloud_identities SET lifecycle = 'DESTROYED'`)
	require.NoError(t, insert(clusterB, nonceB, taken, "INTENT"))

	// One nonce, once.
	err := insert("55555555-5555-5555-5555-555555555555", nonceB, "another", "INTENT")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ux_cloud_identities_ownership_nonce")

	// A second IMPORTED row for one address is refused; REFUSED rows are not limited.
	runID := run(t, db, clusterB, 1)
	imp := func(decision, reason string) error {
		return db.Exec(`INSERT INTO import_records (cluster_uid, run_id, address, decision, reason, actor_id, actor_issuer, request_id, decided_at)
			VALUES (?, ?, 'google_container_cluster.this', ?, ?, 'a', 'i', ?, now())`, clusterB, runID, decision, reason, reqUID).Error
	}
	require.NoError(t, imp("IMPORTED", "VERIFIED"))
	err = imp("IMPORTED", "VERIFIED")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ux_import_records_imported_address")
	require.NoError(t, imp("REFUSED", "NAME_MISMATCH"))
	require.NoError(t, imp("REFUSED", "NAME_MISMATCH"))

	// An operation is recorded once per (cluster, operation name).
	op := func() error {
		return db.Exec(`INSERT INTO cloud_operations (cluster_uid, operation_name, status, source, first_seen_at, last_seen_at)
			VALUES (?, 'operation-1', 'PENDING', 'OBSERVER_LIST', now(), now())`, clusterB).Error
	}
	require.NoError(t, op())
	require.Error(t, op())
}

func TestFleetPGForeignKeys(t *testing.T) {
	db := migrated(t)
	// Children of an unknown cluster are refused.
	refuse(t, db, "fk_writer_slots_cluster_uid", `INSERT INTO writer_slots (cluster_uid, state, updated_at) VALUES (?, 'FREE', now())`, clusterA)
	refuse(t, db, "fk_cluster_observations_cluster_uid", `INSERT INTO cluster_observations (cluster_uid, updated_at) VALUES (?, now())`, clusterA)
	refuse(t, db, "fk_state_heads_cluster_uid", `INSERT INTO state_heads (cluster_uid, lineage, serial, recorded_at) VALUES (?, 'l', 1, now())`, clusterA)
	refuse(t, db, "fk_writer_slot_transitions_cluster_uid", `INSERT INTO writer_slot_transitions (cluster_uid, epoch, to_state, actor_id, actor_issuer, manager_lease_epoch, at)
		VALUES (?, 1, 'HELD', 'a', 'i', 1, now())`, clusterA)
	refuse(t, db, "fk_cloud_operations_cluster_uid", `INSERT INTO cloud_operations (cluster_uid, operation_name, status, source, first_seen_at, last_seen_at)
		VALUES (?, 'o', 'DONE', 'RUNNER_LOG', now(), now())`, clusterA)
	refuse(t, db, "fk_import_records_cluster_uid", `INSERT INTO import_records (cluster_uid, run_id, address, decision, reason, actor_id, actor_issuer, request_id, decided_at)
		VALUES (?, 1, 'a', 'REFUSED', 'NAME_MISMATCH', 'a', 'i', ?, now())`, clusterA, reqUID)

	identity(t, db, clusterA, nonceA)
	runID := run(t, db, clusterA, 1)
	refuse(t, db, "fk_cloud_operations_run_id", `INSERT INTO cloud_operations (cluster_uid, run_id, operation_name, status, source, first_seen_at, last_seen_at)
		VALUES (?, ?, 'o', 'DONE', 'RUNNER_LOG', now(), now())`, clusterA, runID+1000)
	refuse(t, db, "fk_import_records_run_id", `INSERT INTO import_records (cluster_uid, run_id, address, decision, reason, actor_id, actor_issuer, request_id, decided_at)
		VALUES (?, ?, 'a', 'REFUSED', 'NAME_MISMATCH', 'a', 'i', ?, now())`, clusterA, runID+1000, reqUID)

	// A cluster with history cannot be deleted out from under it.
	refuse(t, db, "fk_runs_cluster_uid", `DELETE FROM cloud_identities WHERE cluster_uid = ?`, clusterA)

	// writer_slots.holder_run_uid has NO foreign key: it would be circular with runs.
	slot(t, db, clusterA)
	mustExec(t, db, `UPDATE writer_slots SET state='HELD', holder_run_uid='33333333-3333-3333-3333-333333333333' WHERE cluster_uid=?`, clusterA)
}

func TestFleetPGOneRunPerClusterEpoch(t *testing.T) {
	db := migrated(t)
	identity(t, db, clusterA, nonceA)
	run(t, db, clusterA, 1)
	err := db.Exec(`INSERT INTO runs (run_uid, cluster_uid, epoch, request_id, actor_id, actor_issuer, kind, phase, credential_mode,
		cloud_identity_role, cloud_principal, ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, manager_lease_epoch)
		VALUES (gen_random_uuid(), ?, 1, ?, 'a', 'i', 'CREATE', 'INTENT', 'kind_jwks', 'apply', 'p', 1, 1, 1, 1)`, clusterA, reqUID).Error
	require.Error(t, err)
	require.Contains(t, err.Error(), "ux_runs_cluster_uid_epoch")
}

func TestFleetPGNoTableIsTenantScoped(t *testing.T) {
	db := migrated(t)
	var n int
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'fleet' AND column_name = 'tenant_id'`).Scan(&n).Error)
	require.Zero(t, n, "a fleet table gained a tenant_id column")
	var tables int
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'fleet'`).Scan(&tables).Error)
	require.Equal(t, 9, tables, "the migration is expected to create nine tables")
}
