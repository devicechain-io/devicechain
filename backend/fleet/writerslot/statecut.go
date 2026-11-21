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

// CreateStateRole creates a run's own login role, a MEMBER of the provider role, which owns the
// provider's state schema, tables and indexes (pre-created at onboarding, so the per-run role owns
// nothing). The password is delivered only to that run's Job. This is an external act and is never
// called while a row lock is held.
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
	// The identifiers and the password are validated above, so they are quoted, not parameterised
	// (DDL takes no parameters).
	return m.db.WithContext(ctx).Exec(fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s' IN ROLE "%s"`, role, password, providerRole)).Error
}

// RevokeStateRole is the pg state cut: ALTER ROLE ... NOLOGIN, then pg_terminate_backend for every
// session of the role, then a read of pg_stat_activity showing none. VALID UNTIL is deliberately not
// used: it gates login only and leaves live sessions. It returns the sessions still present, which
// is an error unless zero.
func (m *Manager) RevokeStateRole(ctx context.Context, runUID string) error {
	role, err := StateRoleName(runUID)
	if err != nil {
		return err
	}
	db := m.db.WithContext(ctx)
	if err := db.Exec(fmt.Sprintf(`ALTER ROLE "%s" NOLOGIN`, role)).Error; err != nil {
		return fmt.Errorf("writerslot: revoke login: %w", err)
	}
	var left int64
	for attempt := 0; attempt < 20; attempt++ {
		if err := db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = ?`, role).Error; err != nil {
			return fmt.Errorf("writerslot: terminate sessions: %w", err)
		}
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE usename = ?`, role).Scan(&left).Error; err != nil {
			return fmt.Errorf("writerslot: read sessions: %w", err)
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

// DropStateRole drops the per-run role at release. The role owns nothing, so the drop is clean.
func (m *Manager) DropStateRole(ctx context.Context, runUID string) error {
	role, err := StateRoleName(runUID)
	if err != nil {
		return err
	}
	return m.db.WithContext(ctx).Exec(fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, role)).Error
}
