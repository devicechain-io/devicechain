// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-fleet/schema"
)

// unsettledPredicate is the SQL form of "not settled": the text the schema migration freezes as
// the predicate of the partial index ix_cloud_operations_unsettled, so every count here can use that
// index. A cloud operation is settled when it is DONE and either carries no error or has a recorded
// resolution. A DONE operation that carries an error is NOT settled: it routes the slot to recovery
// or quarantine, and only a recorded resolution settles it.
const unsettledPredicate = schema.UnsettledPredicateV1

// clearSlotSQL resets everything a finished or abandoned holder left on the slot.
const clearSlotSQL = `holder_run_uid = NULL, doubted_from = NULL, uncertain_since = NULL, refresh_cut_at = NULL,
	cloud_cut_proof = NULL, cloud_cut_at = NULL, state_cut_method = NULL, state_cut_at = NULL,
	containment = NULL, containment_observed_at = NULL, quarantine_reason = NULL`

// lifecyclePermits is the part of the admission table that depends on the cluster's lifecycle. A
// TEARING_DOWN cluster admits only DESTROY (itself refused in this build) and RECOVER; a DESTROYED
// one admits nothing. A lifecycle this build does not know was rejected when the identity was read.
func lifecyclePermits(lifecycle, kind string) bool {
	switch lifecycle {
	case LifecycleIntent, LifecycleActive:
		return true
	case LifecycleTearingDown:
		return kind == KindDestroy || kind == KindRecover
	case LifecycleDestroyed:
		return false
	}
	return false
}

func validateSpec(s RunSpec) error {
	if s.ClusterUID == "" || s.RunUID == "" || s.RequestID == "" {
		return admitRefused("cluster uid, run uid and request id are required")
	}
	if !in(s.CredentialMode, ModeKindJWKS, ModeGKEWI) {
		return unknown("credential_mode", s.CredentialMode)
	}
	if !in(s.CloudIdentityRole, "apply", "destroy") {
		return unknown("cloud_identity_role", s.CloudIdentityRole)
	}
	if s.CloudPrincipal == "" {
		return admitRefused("the cloud principal the run impersonates is required")
	}
	for name, d := range map[string]time.Duration{"ksa": s.KsaTokenTTL, "sts": s.StsTokenTTL, "access": s.AccessTokenTTL} {
		if d < time.Second {
			// The residual an EXPIRY proof waits out is the sum of these; each is stored before
			// the Job exists, so a missing one would make the proof's bound meaningless.
			return admitRefused("%s token lifetime must be at least one second", name)
		}
	}
	return nil
}

// Acquire is T1 (and T8a for a RECOVER run): it admits a run, or refuses with the rule that
// refused. A normal run starts only from FREE; a RECOVER run only from EXCLUDED. Intent is
// persisted before submission: first_submit_at is written in this transaction, before any Job
// exists, and the run row carries the residual inputs EXPIRY will later be computed from.
//
// Admission, all of which must hold: the lease is ours; the slot is FREE (EXCLUDED for RECOVER);
// the cluster is managed, or the kind is DRIFT_CHECK; the lifecycle permits the kind; the record
// is not tombstoned; the Observer's last listing is complete and fresh; every cloud operation is
// settled; the kind is not DESTROY.
func (m *Manager) Acquire(ctx context.Context, spec RunSpec) (*Run, error) {
	// DESTROY is in the vocabulary so the admission table is complete. Executing it is a typed
	// refusal in every build of this slice, before anything else is looked at.
	if spec.Kind == KindDestroy {
		return nil, ErrDestroyNotImplemented
	}
	if !in(spec.Kind, allKinds...) {
		return nil, unknown("kind", spec.Kind)
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	actor, err := m.actorOrSystem(spec.Actor)
	if err != nil {
		return nil, err
	}

	var run *Run
	err = m.do(ctx, func(tx *gorm.DB) error {
		// The first acquisition creates the slot. ON CONFLICT DO NOTHING takes no row lock, so
		// this does not break the lock order; the FOR UPDATE below is the real step 2.
		if err := tx.Exec(`INSERT INTO writer_slots (cluster_uid, epoch, state, updated_at)
			VALUES (?, 0, 'FREE', clock_timestamp()) ON CONFLICT (cluster_uid) DO NOTHING`, spec.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: create slot: %w", err)
		}
		slot, err := lockSlot(tx, spec.ClusterUID)
		if err != nil {
			return err
		}
		if slot == nil {
			return fmt.Errorf("writerslot: slot for %s vanished", spec.ClusterUID)
		}

		// Step 3: what the Observer last saw.
		var fresh struct {
			Present  bool
			Complete bool
			Fresh    bool
		}
		if err := tx.Raw(`SELECT true AS present, observer_list_complete AS complete,
				(observer_listed_at IS NOT NULL AND observer_listed_at > clock_timestamp() - (? * interval '1 millisecond')) AS fresh
			FROM cluster_observations WHERE cluster_uid = ? FOR SHARE`, m.cfg.ObservationFreshness.Milliseconds(), spec.ClusterUID).Scan(&fresh).Error; err != nil {
			return fmt.Errorf("writerslot: read observation: %w", err)
		}

		// Step 4.
		id, err := lockIdentity(tx, spec.ClusterUID)
		if err != nil {
			return err
		}

		switch {
		case id.ManagementMode != Managed && spec.Kind != KindDriftCheck:
			return admitRefused("cluster is %s: only DRIFT_CHECK is admitted, not %s", id.ManagementMode, spec.Kind)
		case id.TombstonedAt != nil:
			return admitRefused("the cluster record is tombstoned")
		case !lifecyclePermits(id.Lifecycle, spec.Kind):
			return admitRefused("lifecycle %s does not permit %s", id.Lifecycle, spec.Kind)
		}

		// The state role is the run's own and nobody else's: derived from the run uid on a pg backend,
		// absent on any other. Accepting a caller-named role would let an unrelated NOLOGIN role satisfy
		// the state cut while the role the runner really holds stays usable.
		backend := ""
		if id.StateBackend != nil {
			backend = *id.StateBackend
		}
		switch {
		case backend == BackendPG:
			want, err := StateRoleName(spec.RunUID)
			if err != nil {
				return admitRefused("%v", err)
			}
			if spec.StateRoleName != want {
				return admitRefused("a pg-backend run's state role is %s, not %q", want, spec.StateRoleName)
			}
		case spec.StateRoleName != "":
			return admitRefused("a %q-backend run has no per-run state role", backend)
		}

		var to string
		switch spec.Kind {
		case KindRecover:
			if slot.State != StateExcluded {
				return admitRefused("RECOVER is admitted only from EXCLUDED, slot is %s", slot.State)
			}
			to = StateRecovering
			// The excluded holder's per-run state role must be gone before a recovery run takes the
			// slot: it was cut (NOLOGIN, no session) as proof, but a role that still exists can be
			// made LOGIN again, and the recovery run is about to be handed the state.
			if left, err := staleStateRoles(tx, spec.ClusterUID, spec.RunUID); err != nil {
				return err
			} else if len(left) > 0 {
				return admitRefused("the previous holder's per-run state role %s still exists: drop it (DropStateRole) before a recovery run is admitted", left[0])
			}
		default:
			if slot.State != StateFree {
				return admitRefused("slot is %s, not FREE", slot.State)
			}
			to = StateHeld
		}

		if !fresh.Present || !fresh.Complete || !fresh.Fresh {
			return admitRefused("the Observer has no complete listing newer than %v: could not list is not nothing running", m.cfg.ObservationFreshness)
		}

		// Step 7 is read here only to decide; the Observer's own writes never take the slot row.
		var unsettled int64
		if err := tx.Raw(`SELECT count(*) FROM cloud_operations WHERE cluster_uid = ? AND `+unsettledPredicate, spec.ClusterUID).Scan(&unsettled).Error; err != nil {
			return fmt.Errorf("writerslot: read operations: %w", err)
		}
		if unsettled > 0 {
			return admitRefused("%d cloud operation(s) are not settled", unsettled)
		}

		// Intent precedes submission: first_submit_at is set in the transaction that takes the slot.
		// It is written here, with the identity row this transaction already holds (step 4), before
		// the run is inserted (step 6).
		if err := tx.Exec(`UPDATE cloud_identities SET first_submit_at = COALESCE(first_submit_at, clock_timestamp()), updated_at = clock_timestamp() WHERE cluster_uid = ?`,
			spec.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: record first submit: %w", err)
		}

		epochNow, ok := txEpoch(tx)
		if !ok {
			return errors.New("writerslot: acquire outside a lease-checked transaction")
		}
		epoch := slot.Epoch + 1
		var stateRole *string
		if spec.StateRoleName != "" {
			stateRole = &spec.StateRoleName
		}
		// Step 6.
		if err := tx.Exec(`INSERT INTO runs (run_uid, cluster_uid, epoch, request_id, actor_id, actor_issuer, kind, phase, credential_mode, cloud_identity_role,
				cloud_principal, ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, state_role_name, runner_image_digest, tofu_version, provider_lock_hash,
				module_digest, manager_lease_epoch)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'INTENT', ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
			spec.RunUID, spec.ClusterUID, epoch, spec.RequestID, actor.ID, actor.Issuer, spec.Kind, spec.CredentialMode, spec.CloudIdentityRole,
			spec.CloudPrincipal, int(spec.KsaTokenTTL/time.Second), int(spec.StsTokenTTL/time.Second), int(spec.AccessTokenTTL/time.Second), stateRole,
			spec.RunnerImageDigest, spec.TofuVersion, spec.ProviderLockHash, spec.ModuleDigest, epochNow).Error; err != nil {
			return fmt.Errorf("writerslot: record run: %w", err)
		}
		if err := tx.Exec(`UPDATE writer_slots SET epoch = ?, state = ?, holder_run_uid = ?, doubted_from = NULL, uncertain_since = NULL, refresh_cut_at = NULL,
				cloud_cut_proof = NULL, cloud_cut_at = NULL, state_cut_method = NULL, state_cut_at = NULL, containment = NULL, containment_observed_at = NULL,
				quarantine_reason = NULL, updated_at = clock_timestamp() WHERE cluster_uid = ?`, epoch, to, spec.RunUID, spec.ClusterUID).Error; err != nil {
			return fmt.Errorf("writerslot: take slot: %w", err)
		}
		from := slot.State
		reqID := spec.RequestID
		runUID := spec.RunUID
		if err := m.appendTransition(tx, transition{ClusterUID: spec.ClusterUID, Epoch: epoch, RunUID: &runUID, From: &from, To: to,
			Reason: "acquire " + spec.Kind, Actor: actor, RequestID: &reqID}); err != nil {
			return err
		}
		run = &Run{RunUID: spec.RunUID, ClusterUID: spec.ClusterUID, Epoch: epoch, Kind: spec.Kind}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}
