// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/rdb"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-user-management/iam"
)

// BreakGlassActor is the audit actor of a reset run from inside a pod. It names the route, not a
// person: the authority was the operator's cluster credentials, which is all the log can honestly say.
const BreakGlassActor = "break-glass:pod-exec"

var (
	// ErrBreakGlassNotSystem refuses an identity that holds no system role. Break-glass is for the
	// operator accounts the admin API cannot reach (an enrolled sole superuser has nobody to reset
	// it); everyone else is reset through resetIdentityMfa, by an operator, with an audit trail
	// that names that operator.
	ErrBreakGlassNotSystem = errors.New("break-glass is for identities that hold a system role; reset any other identity through the admin API")
	// ErrBreakGlassNeedsPassword refuses a reset that does not rotate the password. The route has no
	// caller to vouch for, so removing the factor alone would hand the account to whoever holds
	// the password (and an operator reaching for break-glass has usually lost the factor, not the password).
	ErrBreakGlassNeedsPassword = errors.New("break-glass always sets a new password; none was given")
)

// BreakGlassResetMfa is the cluster-credential reset of an operator identity's second factor, run
// by the service's own binary inside a pod (dcctl instances break-glass mfa-reset). It is
// ResetMfa's store transaction -- the factor, the recovery codes and the sealed seed go, the
// session epoch rotates, and the password changes in the SAME transaction -- with three refusals the
// admin path does not need: no password, no reset; no system role, no reset; and nothing to remove,
// nothing done. It writes mfa_reset with BreakGlassActor.
//
// There is no request and no caller here, so none of the admin path's context is consulted: the
// only authority is that the process could be started in the pod at all.
func (m *Manager) BreakGlassResetMfa(ctx context.Context, email, newPassword string) (*iam.Identity, error) {
	if newPassword == "" {
		return nil, ErrBreakGlassNeedsPassword
	}
	target, err := m.iam.IdentityForAdmin(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIdentityNotFound
		}
		return nil, err
	}
	if len(target.SystemRoles) == 0 {
		return nil, ErrBreakGlassNotSystem
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if err := m.iam.ResetMfa(ctx, m.secrets, target, string(hash)); err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			return nil, ErrMfaNotEnrolled
		}
		return nil, err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaReset, BreakGlassActor, "", target.Email)
	m.countMfaEnrollment("reset")
	// The epoch has already ended every session; clearing the records is best effort and a no-op in
	// a process that holds no session store.
	m.endSessionsBestEffort(target.Email)
	return target, nil
}
