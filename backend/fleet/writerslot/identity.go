// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Import decisions and reasons. The vocabulary is frozen into a versioned CHECK on import_records.
const (
	DecisionImported = "IMPORTED"
	DecisionRefused  = "REFUSED"

	ReasonVerified              = "VERIFIED"
	ReasonNameMismatch          = "NAME_MISMATCH"
	ReasonProjectMismatch       = "PROJECT_MISMATCH"
	ReasonLocationMismatch      = "LOCATION_MISMATCH"
	ReasonNonceMismatch         = "NONCE_MISMATCH"
	ReasonCreateBeforeIntent    = "CREATE_TIME_BEFORE_INTENT"
	ReasonIdentityMismatch      = "IDENTITY_MISMATCH"
	ReasonPoolParentNotVerified = "POOL_PARENT_NOT_VERIFIED"
	ReasonPoolNotDeclared       = "POOL_NOT_DECLARED"
)

// Intent is the create intent recorded BEFORE submission. It comes from the onboarding record and
// the cloud_identities row, never from operation input.
type Intent struct {
	Project         string
	Location        string
	CloudName       string
	Nonce           string
	FirstSubmitAt   time.Time
	CloudResourceID string // empty until the first successful read
}

// ObservedCluster is what the cloud lookup found.
type ObservedCluster struct {
	Project    string
	Location   string
	Name       string
	NonceLabel string
	CreateTime time.Time
	ResourceID string
}

// CheckClusterIdentity is the pure identity check that stands between "the cloud has a cluster
// with this name" and "that cluster is the one this run created". Name alone is never enough.
// All must hold: name, project and location match the intent; the nonce label matches; the cloud
// createTime is not earlier than first_submit_at minus the skew margin (cloud clock against
// database clock); and, once known, the cluster's API id equals the stored one.
//
// On a first create the id is unknown, so the check reduces to name, nonce and create time, and is
// only as strong as the nonce's secrecy before submission. After the first successful read the id
// is set once and the check no longer depends on the nonce.
func CheckClusterIdentity(in Intent, got ObservedCluster) string {
	switch {
	case got.Name != in.CloudName:
		return ReasonNameMismatch
	case got.Project != in.Project:
		return ReasonProjectMismatch
	case got.Location != in.Location:
		return ReasonLocationMismatch
	case in.Nonce == "" || subtle.ConstantTimeCompare([]byte(got.NonceLabel), []byte(in.Nonce)) != 1:
		return ReasonNonceMismatch
	case in.FirstSubmitAt.IsZero() || got.CreateTime.Before(in.FirstSubmitAt.Add(-defaultImportSkew)):
		return ReasonCreateBeforeIntent
	case in.CloudResourceID != "" && got.ResourceID != in.CloudResourceID:
		return ReasonIdentityMismatch
	}
	return ReasonVerified
}

// CheckPoolImport decides whether a node pool may be imported. A pool carries no ownership label,
// so it inherits trust from its parent: the parent path must be the verified cluster (its
// cloud_resource_id matched in the SAME recovery run), and the pool's name must be one this
// cluster's intent declares. Any other pool is reported, never imported.
func CheckPoolImport(verifiedClusterID, parentResourceID, poolName string, declared []string) string {
	if verifiedClusterID == "" || parentResourceID != verifiedClusterID {
		return ReasonPoolParentNotVerified
	}
	if !in(poolName, declared...) {
		return ReasonPoolNotDeclared
	}
	return ReasonVerified
}

// ClusterImportDecision is what RecordClusterImport decided.
type ClusterImportDecision struct {
	Decision string
	Reason   string
}

// RecordClusterImport records the identity-check decision for a cluster address during a RECOVER
// run. The intent comes from the database. An all-match decision is IMPORTED and, the first time,
// sets cloud_resource_id and recovered_via once; a mismatch of the id, or a create time before the
// intent, is REFUSED and quarantines the slot; every other mismatch is REFUSED and recorded. No
// import, no create. Executing the import is ExecuteImport, which is not implemented.
func (m *Manager) RecordClusterImport(ctx context.Context, runUID, address string, got ObservedCluster, actor Actor, requestID string) (ClusterImportDecision, error) {
	a, err := m.actorOrSystem(actor)
	if err != nil {
		return ClusterImportDecision{}, err
	}
	var out ClusterImportDecision
	var quarantined bool
	err = m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateRecovering)
		if err != nil {
			return err
		}
		if run.Kind != KindRecover {
			return transitionRefused("recovery imports belong to a RECOVER run")
		}
		id, err := lockIdentity(tx, slot.ClusterUID)
		if err != nil {
			return err
		}
		if id.ManagementMode != Managed {
			return transitionRefused("an observe-only cluster is never imported into")
		}
		in := Intent{Project: id.ProjectID, Location: id.Location, CloudName: id.CloudName}
		if id.OwnershipNonce != nil {
			in.Nonce = *id.OwnershipNonce
		}
		if id.FirstSubmitAt != nil {
			in.FirstSubmitAt = *id.FirstSubmitAt
		}
		if id.CloudResourceID != nil {
			in.CloudResourceID = *id.CloudResourceID
		}
		reason := CheckClusterIdentity(in, got)
		out = ClusterImportDecision{Decision: DecisionRefused, Reason: reason}
		if reason == ReasonVerified {
			out.Decision = DecisionImported
		}
		evidence, _ := json.Marshal(map[string]any{
			"observed_name": got.Name, "observed_project": got.Project, "observed_location": got.Location,
			"nonce_label_matches": reason != ReasonNonceMismatch && reason != ReasonNameMismatch && reason != ReasonProjectMismatch && reason != ReasonLocationMismatch,
			"create_time":         got.CreateTime.UTC().Format(time.RFC3339Nano), "resource_id": got.ResourceID,
		})
		if err := m.insertImport(tx, slot.ClusterUID, run.ID, address, got.ResourceID, string(evidence), out, a, requestID); err != nil {
			return err
		}
		switch {
		case out.Decision == DecisionImported && id.CloudResourceID == nil:
			if err := tx.Exec(`UPDATE cloud_identities SET cloud_resource_id = ?, recovered_via = 'import', recovered_at = clock_timestamp(), updated_at = clock_timestamp() WHERE cluster_uid = ?`,
				got.ResourceID, slot.ClusterUID).Error; err != nil {
				return fmt.Errorf("writerslot: record resource id: %w", err)
			}
		case reason == ReasonIdentityMismatch:
			quarantined = true
			return m.quarantineTx(tx, slot, &runUID, QuarantineIdentityMismatch, a, map[string]any{"address": address})
		case reason == ReasonCreateBeforeIntent:
			quarantined = true
			return m.quarantineTx(tx, slot, &runUID, QuarantineCreateBeforeIntent, a, map[string]any{"address": address})
		}
		return nil
	})
	if err != nil {
		return ClusterImportDecision{}, err
	}
	if quarantined {
		return out, ErrQuarantined
	}
	return out, nil
}

// RecordPoolImport records the decision for a node pool address of a verified cluster.
func (m *Manager) RecordPoolImport(ctx context.Context, runUID, address, poolName, parentResourceID string, declared []string, actor Actor, requestID string) (ClusterImportDecision, error) {
	a, err := m.actorOrSystem(actor)
	if err != nil {
		return ClusterImportDecision{}, err
	}
	var out ClusterImportDecision
	err = m.do(ctx, func(tx *gorm.DB) error {
		slot, run, err := heldBy(tx, runUID, StateRecovering)
		if err != nil {
			return err
		}
		id, err := lockIdentity(tx, slot.ClusterUID)
		if err != nil {
			return err
		}
		// "Verified in the same recovery run": the cluster has an IMPORTED decision from THIS run.
		var verified int64
		if err := tx.Raw(`SELECT count(*) FROM import_records WHERE cluster_uid = ? AND run_id = ? AND decision = 'IMPORTED' AND address LIKE 'google_container_cluster.%'`,
			slot.ClusterUID, run.ID).Scan(&verified).Error; err != nil {
			return fmt.Errorf("writerslot: read import records: %w", err)
		}
		verifiedID := ""
		if verified > 0 && id.CloudResourceID != nil {
			verifiedID = *id.CloudResourceID
		}
		reason := CheckPoolImport(verifiedID, parentResourceID, poolName, declared)
		out = ClusterImportDecision{Decision: DecisionRefused, Reason: reason}
		if reason == ReasonVerified {
			out.Decision = DecisionImported
		}
		evidence, _ := json.Marshal(map[string]any{"pool": poolName, "parent": parentResourceID})
		return m.insertImport(tx, slot.ClusterUID, run.ID, address, "", string(evidence), out, a, requestID)
	})
	return out, err
}

func (m *Manager) insertImport(tx *gorm.DB, clusterUID string, runID uint, address, cloudID, evidence string, d ClusterImportDecision, a Actor, requestID string) error {
	if requestID == "" {
		return fmt.Errorf("writerslot: an import decision carries the request id")
	}
	err := tx.Exec(`INSERT INTO import_records (cluster_uid, run_id, address, cloud_id, evidence, decision, reason, actor_id, actor_issuer, request_id, decided_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?::jsonb, ?, ?, ?, ?, ?, clock_timestamp())`,
		clusterUID, runID, address, cloudID, evidence, d.Decision, d.Reason, a.ID, a.Issuer, requestID).Error
	if err != nil {
		return fmt.Errorf("writerslot: record import decision: %w", err)
	}
	return nil
}

// ExecuteImport would run the import for an IMPORTED decision. Until the runner exists it fails
// loudly: a recognised, decided import that nothing can execute is a typed error, never a
// plausible success.
func (m *Manager) ExecuteImport(context.Context, string, string) error {
	return ErrImportNotImplemented
}
