// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// heldBy loads the slot and run for a transition that acts on the slot's CURRENT holder, in lock
// order (slot, then run), and requires that the slot's holder is this run.
func heldBy(tx *gorm.DB, runUID string, states ...string) (*slotRow, *runRow, error) {
	slot, run, _, _, err := heldByLocked(tx, runUID, lockNone, states...)
	return slot, run, err
}

// What a transition locks between the slot (step 2) and the run (step 6), in lock order: the
// identity (step 4) and the state head (step 5). A transition that needs either takes it HERE, not
// after it has loaded the run, because a lock taken after a later step is an opposing order.
type lockSet int

const (
	lockNone        lockSet = 0
	lockIdentityRow lockSet = 1 << iota
	lockHeadRow
)

// heldByLocked is heldBy that also takes the identity and/or state head in lock order.
func heldByLocked(tx *gorm.DB, runUID string, locks lockSet, states ...string) (*slotRow, *runRow, *identityRow, *StateHead, error) {
	cluster, err := slotForRun(tx, runUID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	slot, err := lockSlot(tx, cluster)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if slot == nil {
		return nil, nil, nil, nil, transitionRefused("no slot for cluster %s", cluster)
	}
	var id *identityRow
	if locks&lockIdentityRow != 0 {
		if id, err = lockIdentity(tx, cluster); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	var head *StateHead
	if locks&lockHeadRow != 0 {
		if head, err = lockHead(tx, cluster); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	run, err := loadRun(tx, runUID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !in(slot.State, states...) {
		return slot, run, id, head, transitionRefused("slot is %s, want one of %v", slot.State, states)
	}
	if slot.HolderRunUID == nil || *slot.HolderRunUID != runUID {
		return slot, run, id, head, transitionRefused("run %s is not the slot's holder", runUID)
	}
	return slot, run, id, head, nil
}

// staleStateRoles lists the per-run state roles of this cluster's OTHER runs that still exist in
// the catalog. A role outlives its run until DropStateRole, and a surviving role can be given LOGIN
// again.
func staleStateRoles(tx *gorm.DB, clusterUID, exceptRunUID string) ([]string, error) {
	var roles []string
	if err := tx.Raw(`SELECT r.state_role_name FROM runs r JOIN pg_roles p ON p.rolname = r.state_role_name
		WHERE r.cluster_uid = ? AND r.run_uid <> ? AND r.state_role_name IS NOT NULL`, clusterUID, exceptRunUID).Scan(&roles).Error; err != nil {
		return nil, fmt.Errorf("writerslot: read roles: %w", err)
	}
	return roles, nil
}

// quarantineTx moves the slot to QUARANTINED inside an open transaction. It leaves the holder in
// place: the recovery operator needs to know whose proof is missing.
func (m *Manager) quarantineTx(tx *gorm.DB, slot *slotRow, runUID *string, reason string, actor Actor, evidence map[string]any) error {
	if !in(reason, QuarantineNoProof, QuarantineIdentityMismatch, QuarantineStateHeadMismatch, QuarantineForeignLockHolder,
		QuarantineUnsettledOperation, QuarantineSecondPod, QuarantineCreateBeforeIntent) {
		return unknown("quarantine_reason", reason)
	}
	if slot.State == StateQuarantined {
		return nil
	}
	if err := tx.Exec(`UPDATE writer_slots SET state = 'QUARANTINED', quarantine_reason = ?, updated_at = clock_timestamp() WHERE cluster_uid = ?`,
		reason, slot.ClusterUID).Error; err != nil {
		return fmt.Errorf("writerslot: quarantine: %w", err)
	}
	from := slot.State
	return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: runUID, From: &from, To: StateQuarantined,
		Reason: reason, EvidenceMap: evidence, Actor: actor})
}

// Quarantine is T7: no proof is obtainable, or an identity, state-head or lock mismatch was found.
// A quarantined slot leaves only through Prove: there is no waiver.
func (m *Manager) Quarantine(ctx context.Context, clusterUID, reason string, actor Actor) error {
	a, err := m.actorOrSystem(actor)
	if err != nil {
		return err
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		slot, err := lockSlot(tx, clusterUID)
		if err != nil {
			return err
		}
		if slot == nil {
			return transitionRefused("no slot for cluster %s", clusterUID)
		}
		return m.quarantineTx(tx, slot, slot.HolderRunUID, reason, a, nil)
	})
}

// Observe is T2: the Job and its single pod exist. A SECOND pod UID for the same Job quarantines,
// because a second pod under one run would be a second writer inside one epoch.
func (m *Manager) Observe(ctx context.Context, runUID, jobNamespace, jobUID, podUID, nodeName string) error {
	if jobUID == "" || podUID == "" {
		return transitionRefused("a job uid and a pod uid are required")
	}
	var quarantined bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		// UNCERTAIN and QUARANTINED are admitted here for ONE reason: a pod UID that is not the
		// recorded one is evidence of a second writer, and that evidence is what a later TERMINATION
		// proof (and now EXPIRY) must account for, so it is appended whatever the slot's state.
		slot, run, err := heldBy(tx, runUID, StateHeld, StateRecovering, StateUncertain, StateQuarantined)
		if err != nil {
			return err
		}
		if run.PodUID != nil && *run.PodUID != podUID {
			quarantined = true
			secondPod := map[string]any{"recorded_pod_uid": *run.PodUID, "observed_pod_uid": podUID}
			if slot.State == StateQuarantined {
				// Already quarantined (for any reason, which stays): only the evidence row is new.
				st := slot.State
				return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &st, To: StateQuarantined,
					Reason: QuarantineSecondPod, EvidenceMap: secondPod, Actor: Actor{ID: m.holderID, Issuer: reservedSystemIssuer}})
			}
			return m.quarantineTx(tx, slot, &runUID, QuarantineSecondPod, Actor{ID: m.holderID, Issuer: reservedSystemIssuer}, secondPod)
		}
		if !in(slot.State, StateHeld, StateRecovering) {
			return transitionRefused("slot is %s: only a pod other than the recorded one is recorded against it", slot.State)
		}
		if !in(run.Phase, PhaseIntent, PhaseJobCreating, PhaseRunning) {
			return transitionRefused("run is %s", run.Phase)
		}
		// A recovery run starts only from a state head that was CHECKED: the state it reads must
		// have the lineage on record and a serial no lower, or it could overwrite state before
		// anything noticed. See VerifyRecoveryStart.
		if run.Kind == KindRecover && run.Phase == PhaseIntent {
			return transitionRefused("a RECOVER run's starting state head has not been verified (VerifyRecoveryStart)")
		}
		if run.JobUID != nil && *run.JobUID != jobUID {
			return transitionRefused("run %s already has a different Job", runUID)
		}
		return tx.Exec(`UPDATE runs SET phase = 'RUNNING', job_namespace = ?, job_uid = ?, pod_uid = ?, node_name = NULLIF(?, ''),
			started_at = COALESCE(started_at, clock_timestamp()), last_observed_at = clock_timestamp() WHERE run_uid = ?`,
			jobNamespace, jobUID, podUID, nodeName, runUID).Error
	})
	if err != nil {
		return err
	}
	if quarantined {
		return ErrQuarantined
	}
	return nil
}

// Exit is T3: every container of the recorded pod UID is reported terminated. Pod absence, pod
// phase and disruption conditions are not representable in the evidence. The slot stays HELD (or
// RECOVERING); only Release or FinishRecovery frees it. The state head the run wrote is recorded,
// and a head with a different lineage or a lower serial than the one on record quarantines.
func (m *Manager) Exit(ctx context.Context, runUID string, spec ExitSpec) error {
	var quarantined bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		// The state head (step 5) is locked ahead of the run (step 6).
		slot, run, _, head, err := heldByLocked(tx, runUID, lockHeadRow, StateHeld, StateRecovering)
		if err != nil {
			return err
		}
		if !in(run.Phase, PhaseRunning, PhaseJobCreating, PhaseIntent) {
			return transitionRefused("run is %s", run.Phase)
		}
		if run.PodUID == nil || !spec.Pod.AllTerminated(*run.PodUID) {
			return transitionRefused("every container of the recorded pod must be reported terminated; absence, phase or conditions are not evidence")
		}
		if spec.Head.Lineage == "" {
			return transitionRefused("the state head's lineage is required")
		}
		if head != nil && (head.Lineage != spec.Head.Lineage || spec.Head.Serial < head.Serial) {
			quarantined = true
			return m.quarantineTx(tx, slot, &runUID, QuarantineStateHeadMismatch, Actor{ID: m.holderID, Issuer: reservedSystemIssuer},
				map[string]any{"recorded_lineage": head.Lineage, "recorded_serial": head.Serial, "observed_lineage": spec.Head.Lineage, "observed_serial": spec.Head.Serial})
		}
		if err := tx.Exec(`INSERT INTO state_heads (cluster_uid, lineage, serial, recorded_run_uid, recorded_at)
			VALUES (?, ?, ?, ?, clock_timestamp())
			ON CONFLICT (cluster_uid) DO UPDATE SET serial = EXCLUDED.serial, recorded_run_uid = EXCLUDED.recorded_run_uid, recorded_at = EXCLUDED.recorded_at`,
			slot.ClusterUID, spec.Head.Lineage, spec.Head.Serial, runUID).Error; err != nil {
			return fmt.Errorf("writerslot: record state head: %w", err)
		}
		phase := PhaseExitedFailed
		if spec.Succeeded {
			phase = PhaseExitedOK
		}
		return tx.Exec(`UPDATE runs SET phase = ?, ended_at = clock_timestamp(), last_observed_at = clock_timestamp() WHERE run_uid = ?`, phase, runUID).Error
	})
	if err != nil {
		return err
	}
	if quarantined {
		return ErrQuarantined
	}
	return nil
}

// ReconcileStateHead is the recorded OPERATOR reconciliation that is the only way a state head's
// lineage may change or its serial may fall. It appends a transition naming the operator, so the
// regression is on the audit trail rather than silent.
func (m *Manager) ReconcileStateHead(ctx context.Context, clusterUID string, head StateHead, operator Actor, reason string) error {
	if operator.ID == "" || operator.Issuer == "" {
		return errors.New("writerslot: a state-head reconciliation names an operator (id and issuer)")
	}
	if reason == "" || head.Lineage == "" {
		return errors.New("writerslot: a reason and a lineage are required")
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		slot, err := lockSlot(tx, clusterUID)
		if err != nil {
			return err
		}
		if slot == nil {
			return transitionRefused("no slot for cluster %s", clusterUID)
		}
		if err := tx.Exec(`INSERT INTO state_heads (cluster_uid, lineage, serial, recorded_run_uid, recorded_at) VALUES (?, ?, ?, NULL, clock_timestamp())
			ON CONFLICT (cluster_uid) DO UPDATE SET lineage = EXCLUDED.lineage, serial = EXCLUDED.serial, recorded_run_uid = NULL, recorded_at = EXCLUDED.recorded_at`,
			clusterUID, head.Lineage, head.Serial).Error; err != nil {
			return fmt.Errorf("writerslot: reconcile state head: %w", err)
		}
		st := slot.State
		return m.appendTransition(tx, transition{ClusterUID: clusterUID, Epoch: slot.Epoch, From: &st, To: slot.State, Reason: "STATE_HEAD_RECONCILED: " + reason,
			EvidenceMap: map[string]any{"lineage": head.Lineage, "serial": head.Serial}, Actor: operator})
	})
}

// settleCheck decides whether the cluster's cloud operations allow a release. An operation DONE
// with an error and no resolution quarantines (T7); any other unsettled operation just refuses.
func (m *Manager) settleCheck(tx *gorm.DB, slot *slotRow, runUID string) (quarantine bool, err error) {
	var errored, other int64
	if err := tx.Raw(`SELECT count(*) FROM cloud_operations WHERE cluster_uid = ? AND status = 'DONE' AND error_code IS NOT NULL AND resolution IS NULL`, slot.ClusterUID).Scan(&errored).Error; err != nil {
		return false, fmt.Errorf("writerslot: read operations: %w", err)
	}
	if errored > 0 {
		if err := m.quarantineTx(tx, slot, &runUID, QuarantineUnsettledOperation, Actor{ID: m.holderID, Issuer: reservedSystemIssuer},
			map[string]any{"errored_operations": errored}); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := tx.Raw(`SELECT count(*) FROM cloud_operations WHERE cluster_uid = ? AND `+unsettledPredicate, slot.ClusterUID).Scan(&other).Error; err != nil {
		return false, fmt.Errorf("writerslot: read operations: %w", err)
	}
	if other > 0 {
		return false, transitionRefused("%d cloud operation(s) are not settled", other)
	}
	return false, nil
}

// stateRoleGone requires that the run's per-run state role has been dropped.
func stateRoleGone(tx *gorm.DB, run *runRow) error {
	if run.StateRoleName == nil {
		return nil
	}
	var n int64
	if err := tx.Raw(`SELECT count(*) FROM pg_roles WHERE rolname = ?`, *run.StateRoleName).Scan(&n).Error; err != nil {
		return fmt.Errorf("writerslot: read roles: %w", err)
	}
	if n > 0 {
		return transitionRefused("the per-run state role %s still exists; it is dropped at release", *run.StateRoleName)
	}
	return nil
}

func exited(run *runRow) bool { return in(run.Phase, PhaseExitedOK, PhaseExitedFailed) }

// Release is T4: the run exited, every cloud operation is settled and the per-run state role is
// dropped, so the slot goes FREE. The slot stays HELD until this point even after the exit.
func (m *Manager) Release(ctx context.Context, runUID string) error {
	var quarantined bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateHeld)
		if err != nil {
			return err
		}
		if run.Kind == KindRecover {
			return transitionRefused("a RECOVER run is released by FinishRecovery")
		}
		if !exited(run) {
			return transitionRefused("run is %s, not exited", run.Phase)
		}
		if quarantined, err = m.settleCheck(tx, slot, runUID); quarantined || err != nil {
			return err
		}
		if err := stateRoleGone(tx, run); err != nil {
			return err
		}
		return m.freeSlot(tx, slot, runUID, "release")
	})
	if err != nil {
		return err
	}
	if quarantined {
		return ErrQuarantined
	}
	return nil
}

func (m *Manager) freeSlot(tx *gorm.DB, slot *slotRow, runUID, reason string) error {
	if err := tx.Exec(`UPDATE writer_slots SET state = 'FREE', `+clearSlotSQL+`, updated_at = clock_timestamp() WHERE cluster_uid = ?`, slot.ClusterUID).Error; err != nil {
		return fmt.Errorf("writerslot: free slot: %w", err)
	}
	if err := tx.Exec(`UPDATE runs SET phase = 'RECONCILED' WHERE run_uid = ?`, runUID).Error; err != nil {
		return fmt.Errorf("writerslot: reconcile run: %w", err)
	}
	from := slot.State
	return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &from, To: StateFree, Reason: reason,
		Actor: Actor{ID: m.holderID, Issuer: reservedSystemIssuer}})
}

// Doubt is T5: the pod cannot be observed, the node is NotReady, or the deadline passed. Doubt is
// reversible; exclusion is not. A slot already UNCERTAIN stays so.
func (m *Manager) Doubt(ctx context.Context, runUID, reason string) error {
	return m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateHeld, StateRecovering, StateUncertain)
		if err != nil {
			return err
		}
		if slot.State == StateUncertain {
			return nil
		}
		if err := tx.Exec(`UPDATE writer_slots SET state = 'UNCERTAIN', doubted_from = ?, uncertain_since = clock_timestamp(), updated_at = clock_timestamp() WHERE cluster_uid = ?`,
			slot.State, slot.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: doubt: %w", err)
		}
		if err := tx.Exec(`UPDATE runs SET phase = 'UNCERTAIN' WHERE run_uid = ?`, run.RunUID).Error; err != nil {
			return fmt.Errorf("writerslot: doubt run: %w", err)
		}
		from := slot.State
		return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &from, To: StateUncertain, Reason: reason,
			Actor: Actor{ID: m.holderID, Issuer: reservedSystemIssuer}})
	})
}

// Reaffirm is T5r: the SAME pod UID is observed Running on a Ready node, and no refresh cut has
// been made, so the slot returns to where it was doubted from. Once refresh_cut_at is set this is
// closed: the credentials are being cut and the writer must be proven excluded, not reaffirmed.
func (m *Manager) Reaffirm(ctx context.Context, runUID string, obs PodObservation) error {
	return m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateUncertain)
		if err != nil {
			return err
		}
		if slot.RefreshCutAt != nil {
			return transitionRefused("a refresh cut has been made: reaffirmation is closed")
		}
		if run.PodUID == nil || obs.PodUID == "" || obs.PodUID != *run.PodUID {
			return transitionRefused("the observed pod is not the run's recorded pod")
		}
		if !obs.Running || !obs.NodeReady {
			return transitionRefused("the pod must be observed Running on a Ready node")
		}
		if slot.DoubtedFrom == nil {
			return transitionRefused("the slot does not record where it was doubted from")
		}
		back := *slot.DoubtedFrom
		if err := tx.Exec(`UPDATE writer_slots SET state = ?, doubted_from = NULL, uncertain_since = NULL, updated_at = clock_timestamp() WHERE cluster_uid = ?`,
			back, slot.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: reaffirm: %w", err)
		}
		if err := tx.Exec(`UPDATE runs SET phase = 'RUNNING', last_observed_at = clock_timestamp() WHERE run_uid = ?`, runUID).Error; err != nil {
			return fmt.Errorf("writerslot: reaffirm run: %w", err)
		}
		from := slot.State
		return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &from, To: back, Reason: "reaffirmed: same pod observed Running on a Ready node",
			Actor: Actor{ID: m.holderID, Issuer: reservedSystemIssuer}})
	})
}

// Resume is what a manager that has just started does with a slot held by a non-terminal run: it
// OBSERVES first, and doubts only if the observation fails or shows something else. It never
// excludes: exclusion needs proof, and a restart is not proof of anything.
func (m *Manager) Resume(ctx context.Context, runUID string, obs *PodObservation, observeErr error) error {
	if observeErr != nil || obs == nil {
		return m.Doubt(ctx, runUID, "restart: the pod could not be observed")
	}
	var state string
	var healthy bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateHeld, StateRecovering, StateUncertain)
		if err != nil {
			return err
		}
		state = slot.State
		healthy = run.PodUID != nil && obs.PodUID == *run.PodUID && obs.Running && obs.NodeReady
		return nil
	})
	if err != nil {
		return err
	}
	if !healthy {
		return m.Doubt(ctx, runUID, "restart: the observed pod is not the run's pod Running on a Ready node")
	}
	if state == StateUncertain {
		return m.Reaffirm(ctx, runUID, *obs)
	}
	return nil // HELD or RECOVERING with a healthy pod: nothing to do, and certainly nothing to exclude.
}

// RecordRefreshCut records that the pod object has been force-deleted and the credential refresh
// is cut. ORDER MATTERS to the caller: force-delete the pod object FIRST, then call this. The
// recorded instant starts the EXPIRY residual, so recording it before the delete would start the
// clock while the kubelet could still refresh the token, and EXPIRY would be proven early. (The
// converse, a delete with no recording yet, only delays the proof.) From here the slot cannot be reaffirmed, and an EXPIRY proof's residual runs from now,
// on the database clock.
func (m *Manager) RecordRefreshCut(ctx context.Context, runUID string) error {
	return m.do(ctx, func(tx *gorm.DB) error {
		// A quarantined slot keeps its holder: an operator who force-deletes the pod after the
		// quarantine records the cut here, and the slot's state does not move.
		slot, _, err := heldBy(tx, runUID, StateUncertain, StateQuarantined)
		if err != nil {
			return err
		}
		if slot.RefreshCutAt != nil {
			return nil
		}
		if err := tx.Exec(`UPDATE writer_slots SET refresh_cut_at = clock_timestamp(), updated_at = clock_timestamp() WHERE cluster_uid = ?`, slot.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: record refresh cut: %w", err)
		}
		from := slot.State
		return m.appendTransition(tx, transition{ClusterUID: slot.ClusterUID, Epoch: slot.Epoch, RunUID: &runUID, From: &from, To: slot.State, Reason: "refresh cut recorded: the pod object was force-deleted",
			Actor: Actor{ID: m.holderID, Issuer: reservedSystemIssuer}})
	})
}

// FinishRecovery is T8b: the recovery run exited, the backend lock is resolved, every cloud
// operation is settled and the state head is consistent, so the slot goes FREE. A lock held by
// anyone but the excluded run quarantines; it is never force-unlocked.
func (m *Manager) FinishRecovery(ctx context.Context, runUID string, lock LockResolution) error {
	var quarantined bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		slot, run, id, _, err := heldByLocked(tx, runUID, lockIdentityRow, StateRecovering)
		if err != nil {
			return err
		}
		if run.Kind != KindRecover {
			return transitionRefused("only a RECOVER run finishes recovery")
		}
		if !exited(run) {
			return transitionRefused("run is %s, not exited", run.Phase)
		}
		// A force-unlock is a GCS lock object's resolution. A pg backend's lock is a session advisory
		// lock that vanishes with the session, so "absent" is the only thing that establishes it.
		if lock == LockForceUnlockedFromExcludedRun && (id.StateBackend == nil || *id.StateBackend != BackendGCS) {
			return transitionRefused("a force-unlock resolves a gcs lock; this cluster's backend is not gcs")
		}
		switch lock {
		case LockAbsent, LockForceUnlockedFromExcludedRun:
		case LockForeign:
			quarantined = true
			return m.quarantineTx(tx, slot, &runUID, QuarantineForeignLockHolder, Actor{ID: m.holderID, Issuer: reservedSystemIssuer}, map[string]any{"lock": string(lock)})
		default:
			return unknown("lock resolution", string(lock))
		}
		var head int64
		if err := tx.Raw(`SELECT count(*) FROM state_heads WHERE cluster_uid = ? AND recorded_run_uid = ?`, slot.ClusterUID, runUID).Scan(&head).Error; err != nil {
			return fmt.Errorf("writerslot: read state head: %w", err)
		}
		if head == 0 {
			return transitionRefused("the recovery run recorded no state head: consistency cannot be judged")
		}
		if quarantined, err = m.settleCheck(tx, slot, runUID); quarantined || err != nil {
			return err
		}
		if err := stateRoleGone(tx, run); err != nil {
			return err
		}
		if left, err := staleStateRoles(tx, slot.ClusterUID, runUID); err != nil {
			return err
		} else if len(left) > 0 {
			return transitionRefused("the excluded holder's per-run state role %s still exists; drop it before the slot is freed", left[0])
		}
		return m.freeSlot(tx, slot, runUID, "recovery finished")
	})
	if err != nil {
		return err
	}
	if quarantined {
		return ErrQuarantined
	}
	return nil
}

// VerifyRecoveryStart is the check a RECOVER run needs BEFORE its Job is created: the state it is
// about to read must have the lineage on record and a serial no lower. A different lineage or a
// lower serial quarantines the slot, because a recovery that went ahead could overwrite state before
// anything noticed. Observe refuses a RECOVER run that has not passed it.
func (m *Manager) VerifyRecoveryStart(ctx context.Context, runUID string, head StateHead) error {
	if head.Lineage == "" {
		return transitionRefused("the state head's lineage is required")
	}
	var quarantined bool
	err := m.do(ctx, func(tx *gorm.DB) error {
		slot, run, _, onRecord, err := heldByLocked(tx, runUID, lockHeadRow, StateRecovering)
		if err != nil {
			return err
		}
		if run.Kind != KindRecover || run.Phase != PhaseIntent {
			return transitionRefused("only a RECOVER run at INTENT has a starting head to verify (it is %s, %s)", run.Kind, run.Phase)
		}
		if onRecord != nil && (onRecord.Lineage != head.Lineage || head.Serial < onRecord.Serial) {
			quarantined = true
			return m.quarantineTx(tx, slot, &runUID, QuarantineStateHeadMismatch, Actor{ID: m.holderID, Issuer: reservedSystemIssuer},
				map[string]any{"recorded_lineage": onRecord.Lineage, "recorded_serial": onRecord.Serial, "observed_lineage": head.Lineage, "observed_serial": head.Serial, "at": "recovery start"})
		}
		return tx.Exec(`UPDATE runs SET phase = 'JOB_CREATING' WHERE run_uid = ?`, runUID).Error
	})
	if err != nil {
		return err
	}
	if quarantined {
		return ErrQuarantined
	}
	return nil
}
