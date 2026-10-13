// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// The GraphQL extension codes of the lock-out refusals below, so a client can say what to do
// in its own language rather than show this package's prose.
const (
	CodeSsoEnforceNoConnection    = "SSO_ENFORCE_NO_CONNECTION"
	CodeSsoEnforcedTenantStranded = "SSO_ENFORCED_TENANT_STRANDED"
)

// SsoLockoutError is a refusal that would otherwise lock a tenant's people out: requiring
// single sign-on where no connection serves the tenant, or removing the last connection that
// serves a tenant that requires it. It carries a machine-readable code (graphql-go copies
// Extensions onto the error) and, for a stranding, the tenants it would have stranded. Two
// values are the same refusal (errors.Is) when their codes match.
type SsoLockoutError struct {
	Code    string
	Message string
	// Tenants are the tenants the change would have stranded, sorted; empty otherwise.
	Tenants []string
}

func (e *SsoLockoutError) Error() string {
	if len(e.Tenants) == 0 {
		return e.Message
	}
	return e.Message + " (tenants: " + strings.Join(e.Tenants, ", ") + ")"
}

// Extensions is what a GraphQL client branches on.
func (e *SsoLockoutError) Extensions() map[string]any {
	ext := map[string]any{"code": e.Code}
	if len(e.Tenants) > 0 {
		ext["tenants"] = e.Tenants
	}
	return ext
}

// Is matches another SsoLockoutError with the same code, so a stranding that names its
// tenants still errors.Is ErrSsoEnforcedTenantStranded.
func (e *SsoLockoutError) Is(target error) bool {
	t, ok := target.(*SsoLockoutError)
	return ok && t.Code == e.Code
}

var (
	// ErrSsoEnforceNoConnection refuses requiring single sign-on for a tenant no enabled
	// connection serves: its people would have no way in.
	ErrSsoEnforceNoConnection error = &SsoLockoutError{
		Code:    CodeSsoEnforceNoConnection,
		Message: "no enabled identity provider connection serves this tenant, so requiring single sign-on would lock its people out; connect one first",
	}
	// ErrTenantNotFound is returned for a tenant that does not exist.
	ErrTenantNotFound = rdb.NewNotFound("tenant not found")
	// ErrSsoEnforcedTenantStranded refuses disabling or deleting a connection, or dropping a
	// tenant from it, when that would leave a tenant that requires single sign-on with no
	// enabled connection serving it: the inverse of ErrSsoEnforceNoConnection.
	ErrSsoEnforcedTenantStranded error = &SsoLockoutError{
		Code:    CodeSsoEnforcedTenantStranded,
		Message: "this is the last enabled identity provider connection serving a tenant that requires single sign-on, so the change would lock its people out; stop requiring single sign-on for the tenant or connect another provider first",
	}
)

// refuseStrandingEnforcedTenants refuses a change to conn that would leave a tenant which
// requires single sign-on with no enabled connection serving it. keep is what conn will
// still serve, enabled, once the change is made: nil when it is being disabled or deleted.
//
// Refused rather than resolved by switching the requirement off, because that would quietly
// re-admit password sign-ins to a tenant whose administrator chose to refuse them, as a side
// effect of an edit to something else. Refusing makes the operator take that decision by
// name. It costs incident response one extra call (turn the requirement off, then disable
// the provider); superusers are exempt from the requirement throughout, so the operator is
// never locked out while making it.
//
// The check and the write are not one transaction, so an enforcement switched on at the same
// moment by another administrator can still race it. Both are superuser-only operations on
// the admin API, and the outcome of losing that race is the state this refuses, recoverable
// by the same superuser.
func (m *Manager) refuseStrandingEnforcedTenants(ctx context.Context, conn *iam.IdpConnection, keep []string) error {
	// A disabled or instance connection is not a way into any tenant today, so losing it
	// strands nobody.
	if conn.Kind != iam.IdpKindTenant || !conn.Enabled {
		return nil
	}
	served, err := m.iam.ConnectionTenants(ctx, conn.ID)
	if err != nil {
		return err
	}
	kept := make(map[string]bool, len(keep))
	for _, t := range keep {
		kept[t] = true
	}
	var stranded []string
	for _, tenant := range served {
		if kept[tenant] {
			continue
		}
		t, err := m.iam.TenantByToken(ctx, tenant)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if !t.SsoEnforced || t.PurgeState.Deleted() {
			continue
		}
		n, err := m.iam.CountEnabledConnectionsServing(ctx, tenant, conn.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			stranded = append(stranded, tenant)
		}
	}
	if len(stranded) > 0 {
		sort.Strings(stranded)
		// Returned as itself, not wrapped: graphql-go reads the code off the error the
		// resolver returns, and a wrapper would carry none.
		base := ErrSsoEnforcedTenantStranded.(*SsoLockoutError)
		return &SsoLockoutError{Code: base.Code, Message: base.Message, Tenants: stranded}
	}
	return nil
}

// SetTenantSsoEnforced requires (or stops requiring) that a tenant's people sign in through
// single sign-on. While it is on, a session that began with a password is refused entry to
// the tenant at every place a tenant grant is minted, including the MCP authorize page; a
// superuser is exempt and each of their entries is audited. Turning it on also ends the
// password sessions already inside the tenant at their next refresh.
func (m *Manager) SetTenantSsoEnforced(ctx context.Context, tenant string, enforced bool) (*iam.Tenant, error) {
	t, err := m.iam.TenantByToken(ctx, tenant)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	if enforced {
		if t.PurgeState.Deleted() {
			return nil, ErrTenantNotFound
		}
		n, err := m.iam.CountEnabledConnectionsServing(ctx, tenant, 0)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, ErrSsoEnforceNoConnection
		}
	}
	if err := m.iam.SetTenantSsoEnforced(ctx, t, enforced); err != nil {
		return nil, err
	}
	return t, nil
}

// MaxSessionMaxAge is the longest session ceiling the API accepts: seven days, or the
// configured refresh lifetime when that is shorter. The sign-in record a ceiling is checked
// against lives in a bucket that expires entries after the refresh lifetime, so a longer
// ceiling would promise sessions the store cannot keep.
func (m *Manager) MaxSessionMaxAge() time.Duration {
	limit := time.Duration(iam.MaxSessionMaxAgeSeconds) * time.Second
	ttl := m.refreshTTL
	if ttl <= 0 {
		ttl = auth.DefaultRefreshTTL
	}
	if ttl < limit {
		return ttl
	}
	return limit
}

// SetIdpSessionMaxAge sets how long, counted from the moment our callback accepts the
// sign-in, a session through the connection lasts. It is enforced at every exchange and
// never extended by a refresh. The change reaches sessions already open: each is judged
// against the connection's current value at its next exchange.
func (m *Manager) SetIdpSessionMaxAge(ctx context.Context, token string, seconds int) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	max := m.MaxSessionMaxAge()
	if seconds < iam.MinSessionMaxAgeSeconds || time.Duration(seconds)*time.Second > max {
		return nil, invalid("the session ceiling is %s to %s (%d to %d seconds)",
			(time.Duration(iam.MinSessionMaxAgeSeconds) * time.Second).String(), max.String(),
			iam.MinSessionMaxAgeSeconds, int(max/time.Second))
	}
	if err := m.iam.SetIdpSessionMaxAge(ctx, conn, seconds); err != nil {
		return nil, err
	}
	return m.IdpConnection(ctx, token)
}
