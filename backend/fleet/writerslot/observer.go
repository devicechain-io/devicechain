// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Observation is what the Observer last saw of a cluster: the outcome of one listing. It is kept
// on its own row so that observation does not contend with the slot lock, and the Observer's
// writes take only the lease (step 1), the observation row (step 3) and the operations (step 7).
type Observation struct {
	// Complete is false on a partial list or a paging error. An incomplete listing never admits a
	// run: "could not list" is not "nothing running".
	Complete      bool
	Error         string
	ClusterStatus string
}

// RecordObservation upserts the cluster's observation row, stamped with the database clock.
func (m *Manager) RecordObservation(ctx context.Context, clusterUID string, o Observation) error {
	return m.do(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO cluster_observations (cluster_uid, observer_listed_at, observer_list_complete, observer_error, cluster_status, updated_at)
			VALUES (?, clock_timestamp(), ?, NULLIF(?, ''), NULLIF(?, ''), clock_timestamp())
			ON CONFLICT (cluster_uid) DO UPDATE SET observer_listed_at = EXCLUDED.observer_listed_at, observer_list_complete = EXCLUDED.observer_list_complete,
				observer_error = EXCLUDED.observer_error, cluster_status = EXCLUDED.cluster_status, updated_at = EXCLUDED.updated_at`,
			clusterUID, o.Complete, o.Error, o.ClusterStatus).Error
	})
}

// Operation statuses and sources.
const (
	OpPending  = "PENDING"
	OpRunning  = "RUNNING"
	OpDone     = "DONE"
	OpAborting = "ABORTING"
	OpUnknown  = "UNKNOWN"

	SourceRunnerLog    = "RUNNER_LOG"
	SourceObserverList = "OBSERVER_LIST"

	ResolutionReconciled  = "RECONCILED"
	ResolutionQuarantined = "QUARANTINED"
)

// CloudOperation is one cloud operation seen for a cluster.
type CloudOperation struct {
	Name         string
	Type         string
	TargetLink   string
	Status       string
	ErrorCode    string
	ErrorMessage string // confidential: never logged by this package
	StartTime    *time.Time
	EndTime      *time.Time
	Source       string
	RunUID       string // empty when the Observer found it unattributed
}

// RecordOperation upserts an operation by (cluster, operation name). A DONE operation carrying an
// error is NOT settled: it stays in the admission count until a resolution is recorded.
func (m *Manager) RecordOperation(ctx context.Context, clusterUID string, op CloudOperation) error {
	if op.Name == "" {
		return errors.New("writerslot: an operation name is required")
	}
	if !in(op.Status, OpPending, OpRunning, OpDone, OpAborting, OpUnknown) {
		return unknown("cloud operation status", op.Status)
	}
	if !in(op.Source, SourceRunnerLog, SourceObserverList) {
		return unknown("cloud operation source", op.Source)
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		var runID *uint
		if op.RunUID != "" {
			var ids []uint
			if err := tx.Raw(`SELECT id FROM runs WHERE run_uid = ? AND cluster_uid = ?`, op.RunUID, clusterUID).Scan(&ids).Error; err != nil {
				return fmt.Errorf("writerslot: find run: %w", err)
			}
			if len(ids) == 0 {
				return fmt.Errorf("writerslot: run %s is not a run of cluster %s", op.RunUID, clusterUID)
			}
			runID = &ids[0]
		}
		return tx.Exec(`INSERT INTO cloud_operations (cluster_uid, run_id, operation_name, operation_type, target_link, status, error_code, error_message,
				cloud_start_time, cloud_end_time, source, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, clock_timestamp(), clock_timestamp())
			ON CONFLICT (cluster_uid, operation_name) DO UPDATE SET
				run_id = COALESCE(cloud_operations.run_id, EXCLUDED.run_id), status = EXCLUDED.status, error_code = EXCLUDED.error_code,
				error_message = EXCLUDED.error_message, cloud_start_time = EXCLUDED.cloud_start_time, cloud_end_time = EXCLUDED.cloud_end_time,
				last_seen_at = EXCLUDED.last_seen_at`,
			clusterUID, runID, op.Name, op.Type, op.TargetLink, op.Status, op.ErrorCode, op.ErrorMessage, op.StartTime, op.EndTime, op.Source).Error
	})
}

// ResolveOperation records the resolution that settles a DONE operation carrying an error.
func (m *Manager) ResolveOperation(ctx context.Context, clusterUID, name, resolution string) error {
	if !in(resolution, ResolutionReconciled, ResolutionQuarantined) {
		return unknown("cloud operation resolution", resolution)
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE cloud_operations SET resolution = ? WHERE cluster_uid = ? AND operation_name = ? AND status = 'DONE'`, resolution, clusterUID, name)
		if res.Error != nil {
			return fmt.Errorf("writerslot: resolve operation: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return transitionRefused("no DONE operation %q for cluster %s to resolve", name, clusterUID)
		}
		return nil
	})
}
