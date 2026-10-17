// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The store half of delegated tenant administration and scoped memberships (ADR-081):
// tenant-owned roles, membership scopes, invitations, and the transaction every
// tenant-plane membership change runs in.

// ErrAmbiguousRole refuses a role token that names both a catalogue role and one the
// tenant owns. The two writers each refuse to create the second of such a pair, so this
// is reachable only through a race between them; resolving it either way would grant
// whichever role a query happened to return first.
var ErrAmbiguousRole = errors.New("the role token names both an instance role and one of this tenant's own; " +
	"ask the operator to rename one of them")

// InTransaction runs fn against a Store bound to one database transaction, committing when
// fn returns nil and rolling back otherwise. Store methods that open their own transaction
// nest inside it (a savepoint).
func (s *Store) InTransaction(ctx context.Context, fn func(tx *Store) error) error {
	return s.sys(ctx).Transaction(func(db *gorm.DB) error {
		return fn(&Store{db: &rdb.RdbManager{Database: db}})
	})
}

// LockTenant reads a tenant row FOR UPDATE, serializing every caller that takes the same
// lock until the surrounding transaction ends. The tenant plane takes it before reading
// anything a membership change decides on — so two administrators each removing the other
// cannot both see a second administrator still standing (the last-administrator rule) —
// and the row is the natural, single lock for "this tenant's membership set".
func (s *Store) LockTenant(ctx context.Context, token string) (*Tenant, error) {
	var t Tenant
	if err := s.sys(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token = ?", token).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// ---- roles a tenant may assign ----------------------------------------------------------

// assignableIn restricts a role query to what a tenant may see and assign: tenant-scope
// roles of the operator's catalogue, and the tenant's own. Never another tenant's.
func assignableIn(db *gorm.DB, tenant string) *gorm.DB {
	return db.Where("scope = ? AND (tenant_id IS NULL OR tenant_id = ?)", ScopeTenant, tenant)
}

// AssignableRoles lists the roles a tenant may assign: the catalogue's tenant-scope roles
// and the tenant's own, ordered catalogue first, then by token.
func (s *Store) AssignableRoles(ctx context.Context, tenant string) ([]Role, error) {
	var roles []Role
	err := assignableIn(s.sys(ctx), tenant).
		Order("CASE WHEN tenant_id IS NULL THEN 0 ELSE 1 END").Order("token").
		Find(&roles).Error
	return roles, err
}

// AssignableRolesByTokens resolves role tokens a tenant is assigning: each must name a
// tenant-scope catalogue role or one of the tenant's own, and nothing else. An unknown
// token fails the whole call (gorm.ErrRecordNotFound in the chain, so it answers
// NOT_FOUND), and so does a token naming one of each (ErrAmbiguousRole). Duplicates in the
// input are de-duplicated.
func (s *Store) AssignableRolesByTokens(ctx context.Context, tenant string, tokens []string) ([]Role, error) {
	uniq := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		uniq = append(uniq, t)
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	var roles []Role
	if err := assignableIn(s.sys(ctx), tenant).Where("token IN ?", uniq).Find(&roles).Error; err != nil {
		return nil, err
	}
	byToken := make(map[string]int, len(roles))
	for _, r := range roles {
		byToken[r.Token]++
	}
	for _, t := range uniq {
		switch byToken[t] {
		case 0:
			return nil, fmt.Errorf("no role with token %q is assignable in this tenant: %w", t, gorm.ErrRecordNotFound)
		case 1:
		default:
			return nil, fmt.Errorf("role %q: %w", t, ErrAmbiguousRole)
		}
	}
	return roles, nil
}

// TenantOwnedRole resolves one of a tenant's OWN roles by token, or gorm.ErrRecordNotFound.
// A catalogue role of the same token is never returned.
func (s *Store) TenantOwnedRole(ctx context.Context, tenant, token string) (*Role, error) {
	var r Role
	if err := s.sys(ctx).Where("scope = ? AND tenant_id = ? AND token = ?", ScopeTenant, tenant, token).
		First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// CatalogueRoleTokenTaken reports whether the catalogue holds a tenant-scope role at token.
// The tenant plane refuses a new role at a token the catalogue holds (a member assigned
// "operator" must mean one role), and the admin plane refuses a catalogue role at a token
// any tenant already uses (OwnedRoleTokenTaken), for the same reason seen from the other
// side.
func (s *Store) CatalogueRoleTokenTaken(ctx context.Context, token string) (bool, error) {
	var n int64
	err := s.sys(ctx).Model(&Role{}).
		Where("scope = ? AND token = ? AND tenant_id IS NULL", ScopeTenant, token).Count(&n).Error
	return n > 0, err
}

// OwnedRoleTokenTaken reports whether any tenant owns a tenant-scope role at token.
func (s *Store) OwnedRoleTokenTaken(ctx context.Context, token string) (bool, error) {
	var n int64
	err := s.sys(ctx).Model(&Role{}).
		Where("scope = ? AND token = ? AND tenant_id IS NOT NULL", ScopeTenant, token).Count(&n).Error
	return n > 0, err
}

// MembershipsHoldingRole returns every membership that holds a role, with its tenant roles
// preloaded.
func (s *Store) MembershipsHoldingRole(ctx context.Context, roleID uint) ([]Membership, error) {
	var ms []Membership
	err := s.sys(ctx).Preload("TenantRoles").
		Where("id IN (SELECT membership_id FROM iam_membership_tenant_roles WHERE role_id = ?)", roleID).
		Find(&ms).Error
	return ms, err
}

// ---- memberships ------------------------------------------------------------------------

// SetMembershipScope replaces a membership's scope. An empty scope is stored as NULL — the
// whole tenant — so "scoped to nothing" is never a stored state.
func (s *Store) SetMembershipScope(ctx context.Context, mem *Membership, scope []string) error {
	if len(scope) == 0 {
		scope = nil // the JSON serializer writes a nil slice as SQL NULL
	} else {
		scope = append([]string(nil), scope...)
	}
	// Select names the column so it is written even when the value is nil, which a struct
	// update would otherwise skip as a zero value.
	if err := s.sys(ctx).Model(mem).Select("scope").Updates(&Membership{Scope: scope}).Error; err != nil {
		return err
	}
	mem.Scope = scope
	return nil
}

// AddScopedMembership binds an identity to a tenant with roles and a scope, enabled or not.
func (s *Store) AddScopedMembership(ctx context.Context, identityID uint, tenantID string, roles []Role, scope []string, enabled bool) (*Membership, error) {
	if len(scope) == 0 {
		scope = nil
	}
	mem := &Membership{IdentityID: identityID, TenantId: tenantID, Enabled: enabled, TenantRoles: roles, Scope: scope}
	if err := s.sys(ctx).Create(mem).Error; err != nil {
		return nil, err
	}
	return mem, nil
}

// TenantMember is one membership of a tenant with the identity it binds.
type TenantMember struct {
	Identity   Identity
	Membership Membership
}

// MemberSearchCriteria filters a tenant's member list.
type MemberSearchCriteria struct {
	rdb.Pagination
	// EmailPrefix keeps members whose email starts with it (case-insensitive).
	EmailPrefix string
	// Enabled keeps only enabled (true) or disabled (false) memberships when set.
	Enabled *bool
	// RoleToken keeps members holding a role with this token, whether the operator's or the
	// tenant's own, when set.
	RoleToken string
	// ScopeAnchor keeps members whose scope names this anchor ("customer:<token>" or
	// "group:<token>"), when set. A member with no scope names none, so it is never kept.
	ScopeAnchor string
	// ManagedByIdp keeps only memberships an identity provider manages (true) or manual ones
	// (false) when set.
	ManagedByIdp *bool
}

// TenantMembers returns one page of a tenant's memberships, ordered by email, each with
// its identity and roles. Only that tenant's memberships are ever read.
func (s *Store) TenantMembers(ctx context.Context, tenant string, criteria MemberSearchCriteria) ([]TenantMember, rdb.SearchResultsPagination, error) {
	q := s.sys(ctx).Model(&Membership{}).
		Joins("JOIN iam_identities ON iam_identities.id = iam_memberships.identity_id AND iam_identities.deleted_at IS NULL").
		Where("iam_memberships.tenant_id = ?", tenant)
	if p := strings.TrimSpace(criteria.EmailPrefix); p != "" {
		// ESCAPE is spelled out: SQLite has no default LIKE escape character, so without it
		// an escaped "_" or "%" would match itself only on Postgres.
		q = q.Where(`iam_identities.email LIKE ? ESCAPE '\'`, escapeLike(strings.ToLower(p))+"%")
	}
	if criteria.Enabled != nil {
		q = q.Where("iam_memberships.enabled = ?", *criteria.Enabled)
	}
	if criteria.RoleToken != "" {
		q = q.Where(`iam_memberships.id IN (SELECT mtr.membership_id FROM iam_membership_tenant_roles mtr `+
			`JOIN iam_roles r ON r.id = mtr.role_id WHERE r.token = ? AND r.deleted_at IS NULL `+
			`AND (r.tenant_id IS NULL OR r.tenant_id = ?))`, criteria.RoleToken, tenant)
	}
	if criteria.ScopeAnchor != "" {
		// The scope is a JSON array of anchor strings; each dialect has its own way to ask
		// whether one is an element, and both answer for exactly that element.
		if q.Dialector.Name() == "postgres" {
			encoded, err := json.Marshal([]string{criteria.ScopeAnchor})
			if err != nil {
				return nil, rdb.SearchResultsPagination{}, err
			}
			q = q.Where("iam_memberships.scope @> ?::jsonb", string(encoded))
		} else {
			q = q.Where("EXISTS (SELECT 1 FROM json_each(iam_memberships.scope) WHERE json_each.value = ?)", criteria.ScopeAnchor)
		}
	}
	if criteria.ManagedByIdp != nil {
		if *criteria.ManagedByIdp {
			q = q.Where("iam_memberships.managed_by IS NOT NULL")
		} else {
			q = q.Where("iam_memberships.managed_by IS NULL")
		}
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	var mems []Membership
	if err := q.Preload("TenantRoles").Order("iam_identities.email").
		Scopes(rdb.Paginate(criteria.Pagination)).Find(&mems).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	ids := make([]uint, 0, len(mems))
	for _, m := range mems {
		ids = append(ids, m.IdentityID)
	}
	var identities []Identity
	if len(ids) > 0 {
		if err := s.sys(ctx).Where("id IN ?", ids).Find(&identities).Error; err != nil {
			return nil, rdb.SearchResultsPagination{}, err
		}
	}
	byID := make(map[uint]Identity, len(identities))
	for _, id := range identities {
		byID[id.ID] = id
	}
	out := make([]TenantMember, 0, len(mems))
	for _, m := range mems {
		out = append(out, TenantMember{Identity: byID[m.IdentityID], Membership: m})
	}
	size := criteria.Pagination.EffectivePageSize()
	page := criteria.PageNumber
	if page < 1 {
		page = 1
	}
	start := (page-1)*size + 1
	end := start + int32(len(out)) - 1
	if len(out) == 0 {
		end = start - 1
	}
	return out, rdb.SearchResultsPagination{PageStart: start, PageEnd: end, TotalRecords: int32(total)}, nil
}

// escapeLike escapes LIKE's wildcards so a prefix filter matches literally.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// TenantMemberships returns every membership of a tenant with its roles preloaded — the
// set the last-administrator rule is decided over.
func (s *Store) TenantMemberships(ctx context.Context, tenant string) ([]Membership, error) {
	var ms []Membership
	err := s.sys(ctx).Preload("TenantRoles").Where("tenant_id = ?", tenant).Find(&ms).Error
	return ms, err
}

// ---- invitations ------------------------------------------------------------------------

// CreateInvitation inserts an invitation.
func (s *Store) CreateInvitation(ctx context.Context, inv *Invitation) error {
	return s.sys(ctx).Create(inv).Error
}

// InvitationByDigest resolves an invitation by the digest of its secret, or
// gorm.ErrRecordNotFound.
func (s *Store) InvitationByDigest(ctx context.Context, digest []byte) (*Invitation, error) {
	var inv Invitation
	if err := s.sys(ctx).Where("token_digest = ?", digest).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// LockInvitationByDigest is InvitationByDigest FOR UPDATE, so two acceptances of one link
// serialize and only the first sees it unaccepted.
func (s *Store) LockInvitationByDigest(ctx context.Context, digest []byte) (*Invitation, error) {
	var inv Invitation
	if err := s.sys(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token_digest = ?", digest).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// InvitationsForMemberships returns the invitations of the given memberships, newest
// first, keyed by membership id.
func (s *Store) InvitationsForMemberships(ctx context.Context, membershipIDs []uint) (map[uint][]Invitation, error) {
	out := map[uint][]Invitation{}
	if len(membershipIDs) == 0 {
		return out, nil
	}
	var rows []Invitation
	if err := s.sys(ctx).Where("membership_id IN ?", membershipIDs).
		Order("created_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.MembershipID] = append(out[r.MembershipID], r)
	}
	return out, nil
}

// TenantInvitations returns one page of a tenant's invitations, newest first.
func (s *Store) TenantInvitations(ctx context.Context, tenant string, pag rdb.Pagination) ([]Invitation, rdb.SearchResultsPagination, error) {
	q := s.sys(ctx).Model(&Invitation{}).Where("tenant_id = ?", tenant)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	var rows []Invitation
	if err := q.Order("created_at DESC").Order("id DESC").Scopes(rdb.Paginate(pag)).Find(&rows).Error; err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	size := pag.EffectivePageSize()
	page := pag.PageNumber
	if page < 1 {
		page = 1
	}
	start := (page-1)*size + 1
	end := start + int32(len(rows)) - 1
	if len(rows) == 0 {
		end = start - 1
	}
	return rows, rdb.SearchResultsPagination{PageStart: start, PageEnd: end, TotalRecords: int32(total)}, nil
}

// RevokeOpenInvitations stamps revoked_at on every unaccepted, unrevoked invitation of a
// membership, returning how many it revoked.
func (s *Store) RevokeOpenInvitations(ctx context.Context, membershipID uint, at time.Time) (int64, error) {
	res := s.sys(ctx).Model(&Invitation{}).
		Where("membership_id = ? AND accepted_at IS NULL AND revoked_at IS NULL", membershipID).
		Update("revoked_at", at)
	return res.RowsAffected, res.Error
}

// MarkInvitationAccepted stamps accepted_at on an invitation.
func (s *Store) MarkInvitationAccepted(ctx context.Context, inv *Invitation, at time.Time) error {
	if err := s.sys(ctx).Model(inv).Update("accepted_at", at).Error; err != nil {
		return err
	}
	inv.AcceptedAt = &at
	return nil
}

// EmailOfMembership returns the email of the identity a membership binds.
func (s *Store) EmailOfMembership(ctx context.Context, membershipID uint) (string, error) {
	var emails []string
	if err := s.sys(ctx).Model(&Identity{}).
		Where("id = (SELECT identity_id FROM iam_memberships WHERE id = ?)", membershipID).
		Pluck("email", &emails).Error; err != nil {
		return "", err
	}
	if len(emails) == 0 {
		return "", gorm.ErrRecordNotFound
	}
	return emails[0], nil
}

// MembershipByID resolves a membership by primary key with its tenant roles, or
// gorm.ErrRecordNotFound.
func (s *Store) MembershipByID(ctx context.Context, id uint) (*Membership, error) {
	var m Membership
	if err := s.sys(ctx).Preload("TenantRoles").Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}
