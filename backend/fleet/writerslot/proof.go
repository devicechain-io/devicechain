// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Prove is T6: it records BOTH cuts for the excluded run, and only then is the slot EXCLUDED. It
// accepts a slot that is UNCERTAIN, or QUARANTINED with a holder (quarantine leaves only through
// the same proof checks; no waiver exists). A proof that is not sufficient returns ErrProofRefused
// and changes nothing.
//
// Cut 1, the cloud:
//
//   - TERMINATION: the kubelet reported every container of the recorded pod UID terminated,
//     read while the pod object was held by a manager-owned finalizer. Pod absence, pod phase and
//     disruption conditions are not accepted: PodGC and a node deletion can remove or fail a pod
//     whose process is still running.
//   - EXPIRY: the pod object has been force-deleted (refresh_cut_at) and the run's computed
//     residual R = ksa + sts + access token lifetimes + skew has elapsed on the DATABASE clock. It
//     is accepted only in a credential mode whose refresh cut the premise record says is measured.
//
// Cut 2, the state backend: PROCESS_TERMINATED (implied by TERMINATION), PG_ROLE_REVOKED (the
// per-run login role is NOLOGIN with no session left, verified here against the catalog and
// pg_stat_activity, not taken on trust) or CLOUD_CREDENTIAL (gcs: the state is written with the
// same impersonated identity, so the cloud cut covers it). For a pg backend, EXPIRY requires
// PG_ROLE_REVOKED: the Postgres credential outlives the cloud tokens.
func (m *Manager) Prove(ctx context.Context, runUID string, p Proof) error {
	actor, err := m.actorOrSystem(p.Actor)
	if err != nil {
		return err
	}
	if !in(p.Cloud.Proof, ProofTermination, ProofExpiry) {
		return unknown("cloud cut proof", p.Cloud.Proof)
	}
	if !in(p.State.Method, CutProcessTerminated, CutPGRoleRevoked, CutCloudCredential) {
		return unknown("state cut method", p.State.Method)
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		// The identity (step 4) is locked ahead of the run (step 6).
		slot, run, id, _, err := heldByLocked(tx, runUID, lockIdentityRow, StateUncertain, StateQuarantined)
		if err != nil {
			return err
		}
		backend := ""
		if id.StateBackend != nil {
			backend = *id.StateBackend
		}
		evidence := map[string]any{"cloud_proof": p.Cloud.Proof, "state_cut": p.State.Method, "backend": backend, "credential_mode": run.CredentialMode}

		// Cut 1.
		switch p.Cloud.Proof {
		case ProofTermination:
			if run.PodUID == nil || !p.Cloud.Pod.AllTerminated(*run.PodUID) {
				return proofRefused("TERMINATION needs every container of the recorded pod reported terminated; absence, phase and conditions do not count")
			}
			// Every OTHER pod this run was seen with must be shown terminated too.
			extra, err := otherPods(tx, slot.ClusterUID, runUID, *run.PodUID)
			if err != nil {
				return err
			}
			for _, uid := range extra {
				covered := false
				for _, o := range p.Cloud.MorePods {
					if o.AllTerminated(uid) {
						covered = true
					}
				}
				if !covered {
					return proofRefused("pod %s was also observed for this run and is not shown terminated: proving the recorded pod dead does not exclude it", uid)
				}
			}
			if p.State.Method != CutProcessTerminated {
				return proofRefused("TERMINATION implies the process is dead: the state cut is PROCESS_TERMINATED, not %s", p.State.Method)
			}
			evidence["pod_uid"] = *run.PodUID
			evidence["containers"] = len(p.Cloud.Pod.Containers)
		case ProofExpiry:
			if slot.RefreshCutAt == nil {
				return proofRefused("EXPIRY needs the refresh cut recorded: the pod object must have been force-deleted")
			}
			if !m.cfg.Premises.ExpiryHolds(run.CredentialMode) {
				return proofRefused("EXPIRY is refused in credential mode %s: its refresh cut is not a measured premise", run.CredentialMode)
			}
			if p.State.Method == CutProcessTerminated {
				return proofRefused("EXPIRY cannot imply the process is terminated")
			}
			// A second pod seen under this run is a second writer whose credentials the recorded
			// pod's refresh cut says nothing about. EXPIRY has no per-pod evidence to offer (it is
			// time since ONE recorded cut), so any SECOND_POD evidence refuses it; the way out is
			// TERMINATION with every pod shown terminated.
			recordedPod := ""
			if run.PodUID != nil {
				recordedPod = *run.PodUID
			}
			extra, err := otherPods(tx, slot.ClusterUID, runUID, recordedPod)
			if err != nil {
				return err
			}
			if len(extra) > 0 {
				return proofRefused("EXPIRY is refused: pod %s was also observed for this run and the refresh cut covers only the recorded pod; prove every pod terminated instead", extra[0])
			}
			residual := time.Duration(run.KsaTokenTTLSec+run.StsTokenTTLSec+run.AccessTokenTTLSec)*time.Second + m.cfg.ClockSkewMargin
			var elapsed bool
			if err := tx.Raw(`SELECT clock_timestamp() >= ?::timestamptz + (? * interval '1 millisecond')`, *slot.RefreshCutAt, residual.Milliseconds()).Scan(&elapsed).Error; err != nil {
				return fmt.Errorf("writerslot: residual clock: %w", err)
			}
			if !elapsed {
				return proofRefused("the residual %v since the refresh cut has not elapsed on the database clock", residual)
			}
			evidence["residual_ms"] = residual.Milliseconds()
			evidence["refresh_cut_at"] = slot.RefreshCutAt.UTC().Format(time.RFC3339Nano)
		}

		// Cut 2.
		switch p.State.Method {
		case CutProcessTerminated:
			if p.Cloud.Proof != ProofTermination {
				return proofRefused("PROCESS_TERMINATED is implied only by TERMINATION")
			}
		case CutPGRoleRevoked:
			if backend != BackendPG {
				return proofRefused("PG_ROLE_REVOKED applies to a pg backend, this cluster's is %q", backend)
			}
			want, rerr := StateRoleName(runUID)
			if rerr != nil {
				return proofRefused("%v", rerr)
			}
			// The role revoked must be THE run's: any other NOLOGIN role proves nothing about the
			// one the runner holds.
			if run.StateRoleName == nil || *run.StateRoleName != want {
				return proofRefused("the run's state role is not %s: its revocation cannot be verified", want)
			}
			sessions, err := verifyRoleCut(tx, *run.StateRoleName)
			if err != nil {
				return err
			}
			evidence["role"] = *run.StateRoleName
			evidence["pg_stat_activity_sessions"] = sessions
		case CutCloudCredential:
			if backend != BackendGCS {
				// A pg backend's Postgres credential outlives the cloud tokens, so the cloud cut
				// says nothing about it: on pg, EXPIRY requires PG_ROLE_REVOKED.
				return proofRefused("CLOUD_CREDENTIAL applies to a gcs backend, this cluster's is %q", backend)
			}
		}
		fromState := slot.State
		if err := tx.Exec(`UPDATE writer_slots SET state = 'EXCLUDED', cloud_cut_proof = ?, cloud_cut_at = clock_timestamp(), state_cut_method = ?, state_cut_at = clock_timestamp(),
				doubted_from = NULL, uncertain_since = NULL, quarantine_reason = NULL, updated_at = clock_timestamp() WHERE cluster_uid = ?`,
			p.Cloud.Proof, p.State.Method, slot.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: exclude: %w", err)
		}
		if err := tx.Exec(`UPDATE runs SET phase = 'EXCLUDED' WHERE run_uid = ?`, runUID).Error; err != nil {
			return fmt.Errorf("writerslot: exclude run: %w", err)
		}
		proof, cut := p.Cloud.Proof, p.State.Method
		return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &fromState, To: StateExcluded,
			CloudCutProof: &proof, StateCutMethod: &cut, EvidenceMap: evidence, Reason: "both cuts proven", Actor: actor})
	})
}

// RecordContainment notes that the apply identity was disabled. It is containment, never a proof:
// a recovery run uses the same identity, so recovery needs it re-enabled, and re-enabling may
// revive unexpired tokens. The slot's state is unchanged.
func (m *Manager) RecordContainment(ctx context.Context, runUID string, actor Actor) error {
	a, err := m.actorOrSystem(actor)
	if err != nil {
		return err
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		slot, _, err := heldBy(tx, runUID, StateUncertain, StateQuarantined)
		if err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE writer_slots SET containment = 'IDENTITY_DISABLED', containment_observed_at = clock_timestamp(), updated_at = clock_timestamp() WHERE cluster_uid = ?`,
			slot.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: record containment: %w", err)
		}
		from := slot.State
		return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &from, To: slot.State,
			Reason: "containment recorded: IDENTITY_DISABLED (not a proof)", Actor: a})
	})
}

// verifyRoleCut reads the catalog: the per-run role must exist, must not be able to log in, and
// must have no session left. Visibility of other sessions needs a superuser or pg_read_all_stats.
func verifyRoleCut(tx *gorm.DB, role string) (int64, error) {
	var canLogin []bool
	if err := tx.Raw(`SELECT rolcanlogin FROM pg_roles WHERE rolname = ?`, role).Scan(&canLogin).Error; err != nil {
		return 0, fmt.Errorf("writerslot: read role: %w", err)
	}
	if len(canLogin) == 0 {
		return 0, proofRefused("the per-run role %s does not exist: its revocation cannot be verified", role)
	}
	if canLogin[0] {
		return 0, proofRefused("the per-run role %s can still log in", role)
	}
	var sessions int64
	if err := tx.Raw(`SELECT count(*) FROM pg_stat_activity WHERE usename = ?`, role).Scan(&sessions).Error; err != nil {
		return 0, fmt.Errorf("writerslot: read sessions: %w", err)
	}
	if sessions > 0 {
		return sessions, proofRefused("the per-run role %s still has %d session(s)", role, sessions)
	}
	return 0, nil
}

// otherPods are the pod UIDs, besides the recorded one, that a SECOND-POD quarantine of this run
// observed. They live in the audit trail's evidence, which is append-only.
func otherPods(tx *gorm.DB, clusterUID, runUID, recorded string) ([]string, error) {
	var uids []string
	if err := tx.Raw(`SELECT DISTINCT evidence->>'observed_pod_uid' FROM writer_slot_transitions
		WHERE cluster_uid = ? AND run_uid = ? AND reason = ? AND evidence ? 'observed_pod_uid'`,
		clusterUID, runUID, QuarantineSecondPod).Scan(&uids).Error; err != nil {
		return nil, fmt.Errorf("writerslot: read observed pods: %w", err)
	}
	var out []string
	for _, u := range uids {
		if u != "" && u != recorded {
			out = append(out, u)
		}
	}
	return out, nil
}
