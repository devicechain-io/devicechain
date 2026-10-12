// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"fmt"
)

// MinSessionMaxAgeSeconds and MaxSessionMaxAgeSeconds bound a connection's session ceiling:
// fifteen minutes (one access-token lifetime, below which a ceiling would end a session
// before it could be used) to seven days (the default life of the sign-in record the
// ceiling is checked against; the manager lowers the upper bound further when its refresh
// lifetime is configured shorter).
const (
	MinSessionMaxAgeSeconds = 15 * 60
	MaxSessionMaxAgeSeconds = 7 * 24 * 60 * 60
)

// SetTenantSsoEnforced turns the tenant's "sign in through single sign-on only" requirement
// on or off. It writes that one column, so it can neither be clobbered by nor clobber the
// tenant update path.
func (s *Store) SetTenantSsoEnforced(ctx context.Context, t *Tenant, enforced bool) error {
	if err := s.sys(ctx).Model(t).Update("sso_enforced", enforced).Error; err != nil {
		return err
	}
	t.SsoEnforced = enforced
	return nil
}

// SetIdpSessionMaxAge sets a connection's session ceiling in seconds. The bound is checked
// here as well as at the API so no caller can store a ceiling that would end every session
// at once (zero) or never.
func (s *Store) SetIdpSessionMaxAge(ctx context.Context, c *IdpConnection, seconds int) error {
	if seconds < MinSessionMaxAgeSeconds || seconds > MaxSessionMaxAgeSeconds {
		return fmt.Errorf("%w: the session ceiling is %d to %d seconds",
			ErrInvalidIdpConnection, MinSessionMaxAgeSeconds, MaxSessionMaxAgeSeconds)
	}
	// Update (not Updates on a map) so UpdatedAt moves with it, which every replica's
	// provider cache is keyed on.
	if err := s.sys(ctx).Model(c).Update("session_max_age_seconds", seconds).Error; err != nil {
		return err
	}
	c.SessionMaxAgeSeconds = seconds
	return nil
}

// RotateSessionEpoch gives an identity a fresh session epoch, ending every refresh token,
// identity token and authorization code minted under the old one at its next exchange,
// without touching the password.
func (s *Store) RotateSessionEpoch(ctx context.Context, id *Identity) error {
	epoch := NewSessionEpoch()
	if err := s.sys(ctx).Model(id).Update("session_epoch", epoch).Error; err != nil {
		return err
	}
	id.SessionEpoch = epoch
	return nil
}

// SsoEnforcedAmong returns which of the given tenant tokens require single sign-on.
func (s *Store) SsoEnforcedAmong(ctx context.Context, tokens []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(tokens) == 0 {
		return out, nil
	}
	var enforced []string
	if err := s.sys(ctx).Model(&Tenant{}).
		Where("token IN ? AND sso_enforced = ?", tokens, true).
		Pluck("token", &enforced).Error; err != nil {
		return nil, err
	}
	for _, t := range enforced {
		out[t] = true
	}
	return out, nil
}

// CountEnabledConnectionsServing counts the enabled tenant connections that serve tenant:
// the ways a person could sign in once the tenant requires single sign-on. A non-zero except
// leaves that connection out of the count, so a caller about to disable, delete or narrow it
// can ask what would remain.
func (s *Store) CountEnabledConnectionsServing(ctx context.Context, tenant string, except uint) (int64, error) {
	var n int64
	q := s.sys(ctx).Table("iam_idp_connection_tenants AS ct").
		Joins("JOIN iam_idp_connections AS c ON c.id = ct.connection_id").
		Where("ct.tenant_id = ? AND c.enabled = ? AND c.kind = ?", tenant, true, IdpKindTenant)
	if except != 0 {
		q = q.Where("c.id <> ?", except)
	}
	err := q.Count(&n).Error
	return n, err
}
