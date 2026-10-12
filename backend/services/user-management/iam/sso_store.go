// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Persistence for external-IdP single sign-on. Nothing here is reachable from GraphQL or
// HTTP yet; the slices that serve it call these methods.
//
// Every delete is a HARD delete (the models carry no DeletedAt), and every multi-row
// change runs in one transaction. Cascades are performed EXPLICITLY here as well as
// declared as foreign keys in the migration: the database enforces them on Postgres, and
// the explicit statements are what make the behaviour identical, and testable, wherever
// the foreign keys are not being enforced.

var (
	// ErrDomainTaken refuses a domain already asserted for a connection. One domain belongs
	// to one IdP, instance-wide.
	ErrDomainTaken = errors.New("that domain is already asserted for an identity provider connection")
	// ErrSubjectLinked refuses linking an external account that is already linked.
	ErrSubjectLinked = errors.New("that external account is already linked to an identity")
	// ErrIdentityAlreadyLinked refuses a second external account for an identity already
	// linked on that connection. A different subject presenting a linked identity's email is
	// what a recycled email address looks like, so it needs a person to look at it, not an
	// automatic relink.
	ErrIdentityAlreadyLinked = errors.New("that identity is already linked to a different account on this connection")
	// ErrNotTenantConnection refuses giving an instance connection a tenant set.
	ErrNotTenantConnection = errors.New("only a tenant connection serves tenants")
	// ErrUnknownTenant refuses serving a tenant that does not exist or is being purged.
	ErrUnknownTenant = errors.New("unknown tenant")
	// ErrInvalidIdpConnection carries the reason a connection was refused before any write.
	ErrInvalidIdpConnection = errors.New("invalid identity provider connection")
)

// CreateIdpConnection inserts a connection. It refuses an unknown kind, and fills the two
// columns whose zero value would be wrong: an unset session ceiling is the 12 hour default
// (not "expire immediately"), and an unset email claim is "email".
func (s *Store) CreateIdpConnection(ctx context.Context, c *IdpConnection) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("%w: kind must be %q or %q", ErrInvalidIdpConnection, IdpKindTenant, IdpKindInstance)
	}
	if c.SessionMaxAgeSeconds == 0 {
		c.SessionMaxAgeSeconds = DefaultSessionMaxAgeSeconds
	}
	if c.EmailClaim == "" {
		c.EmailClaim = "email"
	}
	return s.sys(ctx).Create(c).Error
}

// UpdateIdpConnection writes an already-loaded connection back by primary key. UpdatedAt
// moves with it, which is what every replica's provider cache is keyed on.
func (s *Store) UpdateIdpConnection(ctx context.Context, c *IdpConnection) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("%w: kind must be %q or %q", ErrInvalidIdpConnection, IdpKindTenant, IdpKindInstance)
	}
	return s.sys(ctx).Save(c).Error
}

// IdpConnectionByToken resolves a connection by its slug, or gorm.ErrRecordNotFound.
func (s *Store) IdpConnectionByToken(ctx context.Context, token string) (*IdpConnection, error) {
	var c IdpConnection
	if err := s.sys(ctx).Where("token = ?", token).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// IdpConnectionByID resolves a connection by primary key, or gorm.ErrRecordNotFound.
func (s *Store) IdpConnectionByID(ctx context.Context, id uint) (*IdpConnection, error) {
	var c IdpConnection
	if err := s.sys(ctx).First(&c, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListIdpConnections returns every connection, ordered by token.
func (s *Store) ListIdpConnections(ctx context.Context) ([]IdpConnection, error) {
	var out []IdpConnection
	err := s.sys(ctx).Order("token").Find(&out).Error
	return out, err
}

// IdpConnectionByDomain resolves the connection that has been asserted for a domain, or
// gorm.ErrRecordNotFound. The caller passes the normalised form.
func (s *Store) IdpConnectionByDomain(ctx context.Context, domain string) (*IdpConnection, error) {
	var d IdpConnectionDomain
	if err := s.sys(ctx).Where("domain = ?", domain).First(&d).Error; err != nil {
		return nil, err
	}
	return s.IdpConnectionByID(ctx, d.ConnectionID)
}

// DeleteIdpConnection removes a connection and everything hanging off it, in one
// transaction:
//
//   - the memberships it manages are DISABLED and keep their managed_by. Deleting them
//     would discard access history; clearing managed_by would turn what the IdP granted
//     into manual grants that no later login would ever revoke. Disabled and still marked
//     is the only state that cannot be mistaken for something an administrator chose.
//   - its role mappings, tenant set, domains, identity links and pending link requests are
//     deleted.
//   - its sealed client secret (IdpConnectionSecretRef) is deleted IN THE SAME
//     TRANSACTION, through secrets.BindTx. The secret and the row are one fact: written as
//     two commits, a crash between them leaves ciphertext nothing refers to and nothing can
//     find (the secret store has no listing), and leaving the delete to "the caller" is a
//     step the first caller to forget it would leak silently. store is therefore required;
//     a nil one is refused before anything is deleted.
func (s *Store) DeleteIdpConnection(ctx context.Context, store secrets.SecretStore, c *IdpConnection) error {
	if store == nil {
		return errors.New("deleting an identity provider connection needs the secret store its client secret is sealed in")
	}
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		sealed, err := secrets.BindTx(store, tx)
		if err != nil {
			return err
		}
		if err := sealed.Delete(ctx, IdpConnectionSecretRef(c.ID)); err != nil {
			return fmt.Errorf("deleting the connection's client secret: %w", err)
		}
		if err := tx.Model(&Membership{}).Where("managed_by = ?", c.ID).
			Update("enabled", false).Error; err != nil {
			return err
		}
		// Children before parents, so the order is right whether or not the database is
		// enforcing the foreign keys that would also do it.
		for _, model := range []any{
			&IdpRoleMapping{}, &IdpConnectionTenant{}, &IdpConnectionDomain{},
			&IdentityLink{}, &SsoLinkRequest{},
		} {
			if err := tx.Where("connection_id = ?", c.ID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&IdpConnection{}, "id = ?", c.ID).Error
	})
}

// ConnectionTenants returns the tokens of the tenants a connection serves, sorted.
func (s *Store) ConnectionTenants(ctx context.Context, connectionID uint) ([]string, error) {
	var out []string
	err := s.sys(ctx).Model(&IdpConnectionTenant{}).
		Where("connection_id = ?", connectionID).Order("tenant_id").Pluck("tenant_id", &out).Error
	return out, err
}

// ReplaceConnectionTenants makes a tenant connection serve exactly the given tenants.
//
// Only a kind=tenant connection has a tenant set; an instance connection is refused. Every
// named tenant must exist and not be mid-purge. A tenant dropped from the set takes its role
// mappings with it (a mapping may never name a tenant the connection does not serve), and
// the memberships the connection manages there are disabled: the sign-in recompute walks
// only the tenants still served, so nothing else would ever revoke them.
func (s *Store) ReplaceConnectionTenants(ctx context.Context, c *IdpConnection, tenants []string) error {
	if c.Kind != IdpKindTenant {
		return ErrNotTenantConnection
	}
	want := make([]string, 0, len(tenants))
	seen := make(map[string]struct{}, len(tenants))
	for _, t := range tenants {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		want = append(want, t)
	}
	sort.Strings(want)

	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if len(want) > 0 {
			var live []string
			if err := tx.Model(&Tenant{}).
				Where("token IN ? AND purge_state = ?", want, PurgeActive).
				Pluck("token", &live).Error; err != nil {
				return err
			}
			have := make(map[string]struct{}, len(live))
			for _, l := range live {
				have[l] = struct{}{}
			}
			for _, w := range want {
				if _, ok := have[w]; !ok {
					return fmt.Errorf("%w: %q", ErrUnknownTenant, w)
				}
			}
		}

		// The memberships this connection manages in a dropped tenant are DISABLED, keeping
		// managed_by, exactly as a connection delete leaves them. The sign-in recompute walks
		// only the tenants the connection still serves, so a membership left enabled here
		// would keep what the provider's groups granted for good, reachable by any other way
		// into the tenant (a password, another connection), with nothing ever revoking it.
		disable := tx.Model(&Membership{}).Where("managed_by = ?", c.ID)
		if len(want) > 0 {
			disable = disable.Where("tenant_id NOT IN ?", want)
		}
		if err := disable.Update("enabled", false).Error; err != nil {
			return err
		}
		// Mappings first (they reference the tenant rows), then the tenant rows.
		for _, model := range []any{&IdpRoleMapping{}, &IdpConnectionTenant{}} {
			drop := tx.Where("connection_id = ?", c.ID)
			if len(want) > 0 {
				drop = drop.Where("tenant_id NOT IN ?", want)
			}
			if err := drop.Delete(model).Error; err != nil {
				return err
			}
		}
		if len(want) == 0 {
			return nil
		}
		rows := make([]IdpConnectionTenant, 0, len(want))
		for _, w := range want {
			rows = append(rows, IdpConnectionTenant{ConnectionID: c.ID, TenantId: w})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
}

// AddIdpDomain asserts a domain for a connection. The domain must already be the
// normalised, validated form (identity.NormalizeDomain / ValidateDomain); the store refuses
// anything that is visibly not, because a domain stored in two spellings is two domains.
//
// A domain that is already asserted — for this connection or any other — is ErrDomainTaken.
func (s *Store) AddIdpDomain(ctx context.Context, connectionID uint, domain, verifiedBy string) (*IdpConnectionDomain, error) {
	if domain == "" || domain != strings.ToLower(strings.TrimSpace(domain)) || strings.HasSuffix(domain, ".") {
		return nil, fmt.Errorf("%w: domain %q is not in normalised form", ErrInvalidIdpConnection, domain)
	}
	row := &IdpConnectionDomain{
		Domain: domain, ConnectionID: connectionID,
		VerifiedAt: time.Now().UTC(), VerifiedBy: verifiedBy,
	}
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&IdpConnectionDomain{}).Where("domain = ?", domain).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrDomainTaken
		}
		return tx.Create(row).Error
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// RemoveIdpDomain removes a domain from a connection, reporting whether it was there.
func (s *Store) RemoveIdpDomain(ctx context.Context, connectionID uint, domain string) (bool, error) {
	res := s.sys(ctx).Where("connection_id = ? AND domain = ?", connectionID, domain).
		Delete(&IdpConnectionDomain{})
	return res.RowsAffected > 0, res.Error
}

// ListIdpDomains returns a connection's domains, sorted.
func (s *Store) ListIdpDomains(ctx context.Context, connectionID uint) ([]IdpConnectionDomain, error) {
	var out []IdpConnectionDomain
	err := s.sys(ctx).Where("connection_id = ?", connectionID).Order("domain").Find(&out).Error
	return out, err
}

// CountIdpRoleMappingsForRole counts the mappings that name a role — the legible refusal
// that sits in front of the RESTRICT foreign key.
func (s *Store) CountIdpRoleMappingsForRole(ctx context.Context, roleID uint) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&IdpRoleMapping{}).Where("role_id = ?", roleID).Count(&n).Error
	return n, err
}

// LinkBySubject resolves the identity link for an external account, or
// gorm.ErrRecordNotFound. This is the only way a sign-in finds its identity.
func (s *Store) LinkBySubject(ctx context.Context, connectionID uint, subject string) (*IdentityLink, error) {
	var l IdentityLink
	if err := s.sys(ctx).Where("connection_id = ? AND subject = ?", connectionID, subject).First(&l).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

// LinksForIdentity returns every link an identity holds, oldest first.
func (s *Store) LinksForIdentity(ctx context.Context, identityID uint) ([]IdentityLink, error) {
	var out []IdentityLink
	err := s.sys(ctx).Where("identity_id = ?", identityID).Order("id").Find(&out).Error
	return out, err
}

// CreateIdentityLink records an explicit link. It refuses an external account that is
// already linked (ErrSubjectLinked) and an identity that already has a link on the
// connection under a different subject (ErrIdentityAlreadyLinked).
func (s *Store) CreateIdentityLink(ctx context.Context, l *IdentityLink) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&IdentityLink{}).
			Where("connection_id = ? AND subject = ?", l.ConnectionID, l.Subject).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrSubjectLinked
		}
		if err := tx.Model(&IdentityLink{}).
			Where("connection_id = ? AND identity_id = ?", l.ConnectionID, l.IdentityID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrIdentityAlreadyLinked
		}
		return tx.Create(l).Error
	})
}

// DeleteIdentityLink removes an identity's link on a connection, reporting whether there
// was one.
func (s *Store) DeleteIdentityLink(ctx context.Context, identityID, connectionID uint) (bool, error) {
	res := s.sys(ctx).Where("identity_id = ? AND connection_id = ?", identityID, connectionID).
		Delete(&IdentityLink{})
	return res.RowsAffected > 0, res.Error
}

// UpsertLinkRequest records a refused sign-in for a superuser to decide. The same external
// account refused twice is ONE request: the email and digest are refreshed to the latest
// attempt and the row is not duplicated.
func (s *Store) UpsertLinkRequest(ctx context.Context, r *SsoLinkRequest) error {
	return s.sys(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "connection_id"}, {Name: "subject"}},
		DoUpdates: clause.AssignmentColumns([]string{"email", "claims_digest"}),
	}).Create(r).Error
}

// ListSsoLinkRequests returns the pending requests, oldest first.
func (s *Store) ListSsoLinkRequests(ctx context.Context) ([]SsoLinkRequest, error) {
	var out []SsoLinkRequest
	err := s.sys(ctx).Order("created_at, id").Find(&out).Error
	return out, err
}

// SsoLinkRequestByID resolves a request, or gorm.ErrRecordNotFound.
func (s *Store) SsoLinkRequestByID(ctx context.Context, id uint) (*SsoLinkRequest, error) {
	var r SsoLinkRequest
	if err := s.sys(ctx).First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteSsoLinkRequest removes a decided request, reporting whether it was there.
func (s *Store) DeleteSsoLinkRequest(ctx context.Context, id uint) (bool, error) {
	res := s.sys(ctx).Where("id = ?", id).Delete(&SsoLinkRequest{})
	return res.RowsAffected > 0, res.Error
}

// MarkMembershipsManagedBy records that a connection manages an identity's memberships in the
// given tenants: their roles will follow the connection, and deleting the connection disables
// them. It touches only memberships no connection manages yet.
func (s *Store) MarkMembershipsManagedBy(ctx context.Context, identityID uint, tenants []string, connectionID uint) error {
	if len(tenants) == 0 {
		return nil
	}
	return s.sys(ctx).Model(&Membership{}).
		Where("identity_id = ? AND tenant_id IN ? AND managed_by IS NULL", identityID, tenants).
		Update("managed_by", connectionID).Error
}

// IdentityLinkedTo reports whether an identity has a link on a connection.
func (s *Store) IdentityLinkedTo(ctx context.Context, identityID, connectionID uint) (bool, error) {
	var n int64
	err := s.sys(ctx).Model(&IdentityLink{}).
		Where("identity_id = ? AND connection_id = ?", identityID, connectionID).Count(&n).Error
	return n > 0, err
}

// ErrTenantNotServed refuses a role mapping for a tenant the connection does not serve. The
// migration's composite foreign key refuses it too; this is the sentence an administrator can
// act on.
var ErrTenantNotServed = errors.New("the connection does not serve that tenant")

// IdpRoleMappingView is a mapping with the role it names resolved, for display and for the
// sign-in's recompute.
type IdpRoleMappingView struct {
	Mapping IdpRoleMapping
	Role    Role
}

// ListIdpRoleMappings returns a connection's mappings with their roles, ordered by tenant,
// then group, then role.
func (s *Store) ListIdpRoleMappings(ctx context.Context, connectionID uint) ([]IdpRoleMappingView, error) {
	var rows []IdpRoleMapping
	if err := s.sys(ctx).Where("connection_id = ?", connectionID).
		Order("tenant_id, group_value, role_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.RoleID)
	}
	var roles []Role
	if err := s.sys(ctx).Where("id IN ?", ids).Find(&roles).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]Role, len(roles))
	for _, r := range roles {
		byID[r.ID] = r
	}
	out := make([]IdpRoleMappingView, 0, len(rows))
	for _, r := range rows {
		role, ok := byID[r.RoleID]
		if !ok {
			// The RESTRICT foreign key makes this unreachable on a database that enforces it;
			// failing loudly is the right answer where one does not.
			return nil, fmt.Errorf("role mapping %d names role %d, which does not exist", r.ID, r.RoleID)
		}
		out = append(out, IdpRoleMappingView{Mapping: r, Role: role})
	}
	return out, nil
}

// ReplaceIdpRoleMappings makes the given mappings the whole set for (connection, tenant),
// replacing what was there, in one transaction. The tenant must be one the connection serves.
// The mappings' ConnectionID and TenantId are set here, never taken from the caller.
func (s *Store) ReplaceIdpRoleMappings(ctx context.Context, connectionID uint, tenant string, mappings []IdpRoleMapping) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&IdpConnectionTenant{}).
			Where("connection_id = ? AND tenant_id = ?", connectionID, tenant).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: %q", ErrTenantNotServed, tenant)
		}
		if err := tx.Where("connection_id = ? AND tenant_id = ?", connectionID, tenant).
			Delete(&IdpRoleMapping{}).Error; err != nil {
			return err
		}
		if len(mappings) == 0 {
			return nil
		}
		rows := make([]IdpRoleMapping, 0, len(mappings))
		for _, m := range mappings {
			rows = append(rows, IdpRoleMapping{
				ConnectionID: connectionID, TenantId: tenant, GroupValue: m.GroupValue, RoleID: m.RoleID,
			})
		}
		return tx.Create(&rows).Error
	})
}

// CountConnectionsDefaultingToRole counts the connections that grant a role to every person
// they provision or sign in. default_tenant_roles is a list of role tokens inside a json
// column, so no foreign key protects it; the role-delete guard asks here instead.
func (s *Store) CountConnectionsDefaultingToRole(ctx context.Context, token string) (int64, error) {
	conns, err := s.ListIdpConnections(ctx)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, c := range conns {
		for _, t := range c.DefaultTenantRoles {
			if t == token {
				n++
				break
			}
		}
	}
	return n, nil
}

// CreateProvisionedIdentity inserts a new identity and its link in one transaction, so a
// sign-in never leaves an identity no external account can reach, nor a link to nothing. The
// identity is refused (ErrEmailTaken) if the email is already an identity's: provisioning
// never adopts an existing one.
func (s *Store) CreateProvisionedIdentity(ctx context.Context, id *Identity, l *IdentityLink) error {
	if err := requireCanonicalEmail(id.Email); err != nil {
		return err
	}
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&Identity{}).Where("email = ?", id.Email).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrEmailTaken
		}
		if err := tx.Create(id).Error; err != nil {
			return err
		}
		l.IdentityID = id.ID
		return tx.Create(l).Error
	})
}

// ErrEmailTaken refuses provisioning an identity for an address that already has one.
var ErrEmailTaken = errors.New("an identity with that email already exists")

// SyncManagedMembership makes the membership connectionID manages for (identity, tenant)
// match roles, in one transaction:
//
//   - none exists: one is created, managed, when roles is not empty;
//   - one exists and this connection manages it: its roles become roles, and it is enabled
//     exactly when roles is not empty (an empty set DISABLES it; nothing is deleted);
//   - one exists that no connection manages, or another connection does: it is not touched.
//     A manual grant is an administrator's, and another connection's is that connection's.
//
// It reports whether anything changed.
func (s *Store) SyncManagedMembership(ctx context.Context, identityID uint, tenant string, connectionID uint, roles []Role) (bool, error) {
	changed := false
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var mem Membership
		err := tx.Preload("TenantRoles").
			Where("identity_id = ? AND tenant_id = ?", identityID, tenant).First(&mem).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if len(roles) == 0 {
				return nil
			}
			by := connectionID
			changed = true
			return tx.Create(&Membership{
				IdentityID: identityID, TenantId: tenant, Enabled: true, ManagedBy: &by, TenantRoles: roles,
			}).Error
		case err != nil:
			return err
		}
		if mem.ManagedBy == nil || *mem.ManagedBy != connectionID {
			return nil
		}
		want := make(map[uint]struct{}, len(roles))
		for _, r := range roles {
			want[r.ID] = struct{}{}
		}
		same := len(mem.TenantRoles) == len(want)
		for _, r := range mem.TenantRoles {
			if _, ok := want[r.ID]; !ok {
				same = false
			}
		}
		if !same {
			if err := tx.Model(&mem).Association("TenantRoles").Replace(roles); err != nil {
				return err
			}
			changed = true
		}
		if enabled := len(roles) > 0; mem.Enabled != enabled {
			if err := tx.Model(&mem).Update("enabled", enabled).Error; err != nil {
				return err
			}
			changed = true
		}
		return nil
	})
	return changed, err
}
