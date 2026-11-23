// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	roleNameRe     = regexp.MustCompile(`^fleet_run_[0-9a-f]{32}$`)
	providerRoleRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	passwordRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	uuidRe         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// StateRoleName is the per-run Postgres login role's name, derived from the run uid so that it can
// be created, cut and dropped by name alone.
func StateRoleName(runUID string) (string, error) {
	if !uuidRe.MatchString(runUID) {
		return "", fmt.Errorf("writerslot: %q is not a run uid", runUID)
	}
	return "fleet_run_" + strings.ToLower(strings.ReplaceAll(runUID, "-", "")), nil
}

// The three role functions below run inside the lease-checked transaction like every other state
// change: a manager that lost its lease cannot create, cut or drop a role, and each refuses unless
// the run's phase and the slot's state say the act belongs now. Role DDL is transactional in
// Postgres and touches only the manager's own database, so it is not an "external act" that has to
// stay outside the transaction (the cloud and Kubernetes calls still are).

// CreateStateRole creates a run's own login role, a MEMBER of the provider role, which owns the
// provider's state schema, tables and indexes (pre-created at onboarding, so the per-run role owns
// nothing). The password is delivered only to that run's Job. It is accepted only for the slot's
// holder, in INTENT or JOB_CREATING, and only for the role name recorded on the run at admission.
//
// 🔴 The password is part of the DDL text. Postgres logs a statement's text under log_statement =
// 'ddl' (or 'all'), and a CREATE ROLE ... PASSWORD with a cleartext value then reaches the server
// log, where this process cannot redact it. The manager's own connection logs nothing, but the
// server's setting is the operator's: run the fleet database with log_statement = 'none' (or 'mod'
// without ddl), or move to a client-computed SCRAM verifier, before this handles a password that
// matters. The password is also single-use and scoped to one run's state.
func (m *Manager) CreateStateRole(ctx context.Context, runUID, providerRole, password string) error {
	role, err := StateRoleName(runUID)
	if err != nil {
		return err
	}
	if !providerRoleRe.MatchString(providerRole) {
		return errors.New("writerslot: the provider role is not a plain lower-case identifier")
	}
	if !passwordRe.MatchString(password) {
		return errors.New("writerslot: the role password must be 16-128 characters of A-Z a-z 0-9 _ -")
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		_, run, err := heldBy(tx, runUID, StateHeld, StateRecovering)
		if err != nil {
			return err
		}
		if !in(run.Phase, PhaseIntent, PhaseJobCreating) {
			return transitionRefused("a state role is created before the Job exists; run is %s", run.Phase)
		}
		if run.StateRoleName == nil || *run.StateRoleName != role {
			return transitionRefused("the run did not record %s as its state role at admission", role)
		}
		// The identifiers and the password are validated above, so they are quoted, not
		// parameterised (DDL takes no parameters).
		return tx.Exec(fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s' IN ROLE "%s"`, role, password, providerRole)).Error
	})
}

// RevokeStateRole is the pg state cut: ALTER ROLE ... NOLOGIN, then pg_terminate_backend for every
// session of the role, then a read of pg_stat_activity showing none. VALID UNTIL is deliberately not
// used: it gates login only and leaves live sessions. It is accepted only for the holder of an
// UNCERTAIN or QUARANTINED slot. Each attempt is its own lease-checked transaction, so the retry
// wait never holds the lease row; it errors unless no session is left.
func (m *Manager) RevokeStateRole(ctx context.Context, runUID string) error {
	role, err := StateRoleName(runUID)
	if err != nil {
		return err
	}
	var left int64
	for attempt := 0; attempt < 20; attempt++ {
		err := m.do(ctx, func(tx *gorm.DB) error {
			_, run, err := heldBy(tx, runUID, StateUncertain, StateQuarantined)
			if err != nil {
				return err
			}
			if run.StateRoleName == nil || *run.StateRoleName != role {
				return transitionRefused("the run did not record %s as its state role", role)
			}
			if err := tx.Exec(fmt.Sprintf(`ALTER ROLE "%s" NOLOGIN`, role)).Error; err != nil {
				return fmt.Errorf("writerslot: revoke login: %w", err)
			}
			if err := tx.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = ?`, role).Error; err != nil {
				return fmt.Errorf("writerslot: terminate sessions: %w", err)
			}
			if err := tx.Raw(`SELECT count(*) FROM pg_stat_activity WHERE usename = ?`, role).Scan(&left).Error; err != nil {
				return fmt.Errorf("writerslot: read sessions: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if left == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("writerslot: %d session(s) of %s survived termination", left, role)
}

// DropStateRole drops the per-run role at release, or after the run was excluded. The role owns
// nothing, so the drop is clean. It refuses while the run is still live (its role is the writer's
// credential): the run must have exited, been excluded, or been reconciled.
func (m *Manager) DropStateRole(ctx context.Context, runUID string) error {
	role, err := StateRoleName(runUID)
	if err != nil {
		return err
	}
	return m.do(ctx, func(tx *gorm.DB) error {
		cluster, err := slotForRun(tx, runUID)
		if err != nil {
			return err
		}
		if _, err := lockSlot(tx, cluster); err != nil {
			return err
		}
		run, err := loadRun(tx, runUID)
		if err != nil {
			return err
		}
		if !in(run.Phase, PhaseExitedOK, PhaseExitedFailed, PhaseExcluded, PhaseReconciled) {
			return transitionRefused("run is %s: its state role is still the writer's credential", run.Phase)
		}
		if run.StateRoleName == nil || *run.StateRoleName != role {
			return transitionRefused("the run did not record %s as its state role", role)
		}
		return tx.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, role)).Error
	})
}
