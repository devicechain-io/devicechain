// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// Delegated tenant administration (ADR-081): a tenant's own administrators manage its
// members, invitations and the roles it owns, on the tenant's data plane, under rules that
// keep the delegation from becoming an escalation. The operator's admin plane keeps full
// control beside it and is not bound by these rules.
//
// The rules, all server-side and each a named refusal:
//
//   - ESCALATION — a role the caller defines may carry only tenant-tier authorities the
//     caller itself holds, and a role the caller assigns may carry only authorities the
//     caller holds. "*" may be granted only by a holder of "*". Nor may the caller act on
//     a member who holds anything the caller does not: a delegated administrator cannot
//     strip, scope, disable or remove someone above them.
//   - SELF_MODIFICATION — nobody changes their own roles, scope or status, or removes
//     themselves, on this plane.
//   - LAST_ADMIN — the last enabled, unscoped member holding member:write cannot be
//     removed, disabled, scoped or stripped of it, by any route (a role edit included).
//   - MANAGED_BY_IDP — a membership an identity provider manages is read-only here.
//   - READ_ONLY — a catalogue role is the operator's and cannot be edited or deleted by a
//     tenant.
//
// What the caller "holds" is read twice and intersected: from the access token it
// presented, and from its membership as stored now. The token alone would let an
// administrator demoted a minute ago keep granting until it expired; the store alone
// would ignore an OAuth token's capped authorities.

// Wire codes of the tenant-plane refusals.
const (
	CodeEscalation        = "ESCALATION"
	CodeSelfModification  = "SELF_MODIFICATION"
	CodeLastAdmin         = "LAST_ADMIN"
	CodeManagedByIdp      = "MANAGED_BY_IDP"
	CodeReadOnly          = "READ_ONLY"
	CodeInvalidInvitation = "INVALID_INVITATION"
)

// TenantRefusal is a tenant-plane refusal carrying its own wire code. The policy refusals
// (every code but INVALID_INVITATION) are also auth.ErrForbidden to errors.Is, so a caller
// that already handles a refusal handles these, and a resolver that wraps one by mistake
// still answers FORBIDDEN rather than a bare message.
type TenantRefusal struct {
	code string
	msg  string
}

func (e *TenantRefusal) Error() string { return e.msg }

// Code is the refusal's wire code.
func (e *TenantRefusal) Code() string { return e.code }

// Extensions gives the refusal its wire code. graphql-go reads it only from the error a
// resolver returns directly, so the resolvers return these unwrapped.
func (e *TenantRefusal) Extensions() map[string]any { return map[string]any{"code": e.code} }

// Is makes a policy refusal an auth.ErrForbidden.
func (e *TenantRefusal) Is(target error) bool {
	return target == auth.ErrForbidden && e.code != CodeInvalidInvitation
}

func refuse(code, format string, args ...any) *TenantRefusal {
	return &TenantRefusal{code: code, msg: fmt.Sprintf(format, args...)}
}

// ErrInvalidInvitation is the ONE answer for an invitation link that cannot be used:
// unknown, expired, revoked, already accepted, for a tenant that is gone, or presented by
// someone it was not issued to. One code and one sentence, so a link cannot be probed for
// which of those it is.
var ErrInvalidInvitation = &TenantRefusal{code: CodeInvalidInvitation,
	msg: "this invitation link is not valid: it may have expired, been revoked or already been used"}

// InvitationTTL is how long an invitation link stays usable.
const InvitationTTL = 7 * 24 * time.Hour

// ErrMemberNotFound is the refusal for a member this tenant does not have — including an
// identity that exists but belongs only to other tenants, which must be indistinguishable
// from one that does not exist at all.
var ErrMemberNotFound = rdb.NewNotFound("member not found")

// ErrTenantRoleNotFound is the refusal for a role this tenant cannot see.
var ErrTenantRoleNotFound = rdb.NewNotFound("role not found")

// ErrRoleInUse refuses deleting a tenant role while a member still holds it. It is a
// reference refusal — the same code as every other "other records still refer to it".
var ErrRoleInUse = integrity.NewRefusal(integrity.ClassReference,
	"the role is still assigned to members; reassign them first")

// ErrInvitationPending refuses enabling a membership whose invitation has not been
// accepted: enabling it would let the invitee in without ever accepting, and leave a live
// link behind that could still be redeemed.
var ErrInvitationPending = integrity.NewRefusal(integrity.ClassInvalid,
	"the member has not accepted their invitation; reissue or revoke it instead")

// TenantCaller is who is acting on a tenant's plane, as its verified access token says.
type TenantCaller struct {
	Tenant      string
	Email       string
	Authorities []string
	Superuser   bool
}

// tenantCaller reads the caller from the request's verified claims. Every tenant-plane
// method takes its caller from here and from nowhere else, so the tenant it acts in is the
// tenant of the token — a resolver cannot pass a different one, because it passes none.
//
// It admits only a tenant ACCESS token for a tenant, and refuses a scoped one: the
// resolvers gate on Authorize, which refuses a scoped caller already, and this repeats it
// so that the rules below never have to reason about a member who sees part of the tenant
// administering the rest of it (customer self-administration is out of scope).
func tenantCaller(ctx context.Context) (TenantCaller, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return TenantCaller{}, auth.ErrUnauthenticated
	}
	if claims.TokenType != auth.TokenTypeAccess || strings.TrimSpace(claims.Tenant) == "" || claims.Username == "" {
		return TenantCaller{}, auth.ErrForbidden
	}
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return TenantCaller{}, auth.ErrScopeUnsupported
	}
	return TenantCaller{
		Tenant: claims.Tenant, Email: claims.Username,
		Authorities: claims.Authorities, Superuser: claims.ActingAsSuperuser,
	}, nil
}

// ScopeAnchorInput is one anchor of a scope, as the API carries it.
type ScopeAnchorInput struct {
	Kind  string // auth.AnchorKindCustomer or auth.AnchorKindGroup
	Token string
}

// NormalizeScope turns anchors from the API into the stored form ("customer:acme",
// "group:north"), refusing what may not be stored: an unknown kind or a malformed token
// (INVALID_VALUE), or more than auth.MaxScopeAnchors distinct anchors
// (LIMIT_EXCEEDED — refused, never truncated: a truncated scope would silently hide what
// the member was granted). Duplicates collapse. An empty list is nil: the whole tenant.
func NormalizeScope(anchors []ScopeAnchorInput) ([]string, error) {
	out := make([]string, 0, len(anchors))
	seen := map[string]struct{}{}
	for _, a := range anchors {
		switch a.Kind {
		case auth.AnchorKindCustomer, auth.AnchorKindGroup:
			// A GROUP anchor is stored unchecked, as a customer one is: neither is looked up
			// in device-management. A group reaches devices only while it is an access scope
			// there, and a deleted group's token is never handed to another group, so an
			// anchor naming a group that is not (or no longer) one matches nothing.
		default:
			return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("unknown scope anchor kind %q", a.Kind))
		}
		if err := core.ValidateToken(a.Token); err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("scope anchor token %q: %v", a.Token, err))
		}
		anchor := a.Kind + ":" + a.Token
		if _, _, err := auth.ParseAnchor(anchor); err != nil {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
		}
		if _, dup := seen[anchor]; dup {
			continue
		}
		seen[anchor] = struct{}{}
		out = append(out, anchor)
	}
	if len(out) > auth.MaxScopeAnchors {
		return nil, limit.Exceeded("scope anchors", len(out), auth.MaxScopeAnchors)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ---- authority sets ---------------------------------------------------------------------

// authSet is a set of authorities with "*" as "everything at the bearer's tier".
type authSet struct {
	all bool
	set map[string]struct{}
}

func newAuthSet(lists ...[]string) authSet {
	a := authSet{set: map[string]struct{}{}}
	for _, l := range lists {
		for _, x := range l {
			if x == string(auth.AuthorityAll) {
				a.all = true
				continue
			}
			a.set[x] = struct{}{}
		}
	}
	return a
}

func (a authSet) has(x string) bool {
	if a.all {
		return true
	}
	_, ok := a.set[x]
	return ok
}

// intersect is what both sets grant.
func (a authSet) intersect(b authSet) authSet {
	out := authSet{all: a.all && b.all, set: map[string]struct{}{}}
	for x := range a.set {
		if b.has(x) {
			out.set[x] = struct{}{}
		}
	}
	for x := range b.set {
		if a.has(x) {
			out.set[x] = struct{}{}
		}
	}
	return out
}

// missingFrom returns the first authority in list that a does not cover, or "" when it
// covers them all. "*" in list is covered only by a set that is itself "*".
func (a authSet) missingFrom(list []string) string {
	for _, x := range list {
		if x == string(auth.AuthorityAll) {
			if !a.all {
				return x
			}
			continue
		}
		if !a.has(x) {
			return x
		}
	}
	return ""
}

// narrows reports whether moving from one authority set to another takes an authority
// away: true when from grants anything to does not.
func narrows(from, to authSet) bool {
	if from.all {
		return !to.all
	}
	for x := range from.set {
		if !to.has(x) {
			return true
		}
	}
	return false
}

func roleAuthorities(roles []iam.Role) []string {
	var out []string
	for _, r := range roles {
		out = append(out, r.Authorities...)
	}
	return out
}

// ---- the caller ---------------------------------------------------------------------------

// actor is the caller resolved against the store: what it holds now, and which membership
// (if any) is its own in the tenant.
type actor struct {
	identityID uint
	held       authSet
	superuser  bool
}

// resolveActor reads the caller's live grant and intersects it with its token's. A caller
// that is no longer a superuser and has no enabled, unscoped membership in the tenant
// holds nothing here, whatever its token says.
func (s *Service) resolveActor(ctx context.Context, tx *iam.Store, caller TenantCaller) (actor, error) {
	token := newAuthSet(caller.Authorities)
	id, err := tx.IdentityByEmail(ctx, caller.Email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return actor{}, auth.ErrForbidden
	}
	if err != nil {
		return actor{}, err
	}
	if !id.Enabled {
		return actor{}, auth.ErrForbidden
	}
	for _, r := range id.SystemRoles {
		if r.Token == iam.SuperuserRoleToken && caller.Superuser {
			return actor{identityID: id.ID, held: token.intersect(newAuthSet([]string{string(auth.AuthorityAll)})), superuser: true}, nil
		}
	}
	for i := range id.Memberships {
		m := &id.Memberships[i]
		if m.TenantId != caller.Tenant {
			continue
		}
		if !m.Enabled || m.IsScoped() {
			return actor{}, auth.ErrForbidden
		}
		live := newAuthSet(m.TenantAuthorities(), s.viewerBaseline)
		return actor{identityID: id.ID, held: token.intersect(live)}, nil
	}
	return actor{}, auth.ErrForbidden
}

// requireAbove refuses acting on a member who holds anything the caller does not.
func (s *Service) requireAbove(a actor, target *iam.Membership) error {
	if missing := a.held.missingFrom(append(roleAuthorities(target.TenantRoles), s.viewerBaseline...)); missing != "" {
		return refuse(CodeEscalation, "you cannot change a member who holds %q, which you do not hold", missing)
	}
	return nil
}

// requireAssignable refuses assigning a role carrying an authority the caller does not hold.
func requireAssignable(a actor, roles []iam.Role) error {
	for _, r := range roles {
		if missing := a.held.missingFrom(r.Authorities); missing != "" {
			return refuse(CodeEscalation, "you cannot assign role %q: it grants %q, which you do not hold", r.Token, missing)
		}
	}
	return nil
}

// isAdmin reports whether a membership counts toward the last-administrator rule: enabled,
// unscoped, and holding member:write through its roles.
func isAdmin(m *iam.Membership) bool {
	return m.Enabled && !m.IsScoped() && newAuthSet(m.TenantAuthorities()).has(string(auth.MemberWrite))
}

// requireAnAdminRemains refuses a change after which the tenant would have no
// administrator when it had one before. after maps a membership id to its state after the
// change, or nil for a membership the change removes; memberships not in it are unchanged.
func requireAnAdminRemains(before []iam.Membership, after map[uint]*iam.Membership) error {
	had, has := 0, 0
	for i := range before {
		m := &before[i]
		if isAdmin(m) {
			had++
		}
		next, changed := after[m.ID]
		switch {
		case !changed:
			if isAdmin(m) {
				has++
			}
		case next != nil && isAdmin(next):
			has++
		}
	}
	if had > 0 && has == 0 {
		return refuse(CodeLastAdmin, "this would leave the tenant with no member able to manage members")
	}
	return nil
}

// ---- reading members ------------------------------------------------------------------------

// MemberView is one member of a tenant as the tenant plane shows it.
type MemberView struct {
	Identity          iam.Identity
	Membership        iam.Membership
	InvitationPending bool
}

// memberViews decorates memberships with whether an invitation is still open.
func memberViews(ctx context.Context, tx *iam.Store, members []iam.TenantMember) ([]MemberView, error) {
	ids := make([]uint, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.Membership.ID)
	}
	invs, err := tx.InvitationsForMemberships(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(members))
	for _, m := range members {
		out = append(out, MemberView{Identity: m.Identity, Membership: m.Membership,
			InvitationPending: hasOpenInvitation(invs[m.Membership.ID])})
	}
	return out, nil
}

// hasOpenInvitation reports whether any invitation is neither accepted nor revoked —
// expired ones included: the member has not joined.
func hasOpenInvitation(invs []iam.Invitation) bool {
	for _, i := range invs {
		if i.AcceptedAt == nil && i.RevokedAt == nil {
			return true
		}
	}
	return false
}

// Members returns one page of the caller's tenant's members.
func (s *Service) Members(ctx context.Context, criteria iam.MemberSearchCriteria) ([]MemberView, rdb.SearchResultsPagination, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	found, pag, err := s.iam.TenantMembers(ctx, caller.Tenant, criteria)
	if err != nil {
		return nil, pag, err
	}
	views, err := memberViews(ctx, s.iam, found)
	return views, pag, err
}

// Member returns one member of the caller's tenant by email. An email that names no
// member of THIS tenant — whether or not the identity exists elsewhere — is NOT_FOUND.
func (s *Service) Member(ctx context.Context, email string) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

func (s *Service) memberIn(ctx context.Context, tx *iam.Store, tenant, email string) (*MemberView, error) {
	id, mem, err := loadTenantMember(ctx, tx, tenant, email)
	if err != nil {
		return nil, err
	}
	views, err := memberViews(ctx, tx, []iam.TenantMember{{Identity: *id, Membership: *mem}})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// loadTenantMember resolves an identity's membership in a tenant, answering NOT_FOUND —
// the same answer — for an email that is malformed, unknown, or a member elsewhere only.
func loadTenantMember(ctx context.Context, tx *iam.Store, tenant, email string) (*iam.Identity, *iam.Membership, error) {
	canonical, err := iam.CanonicalEmail(email)
	if err != nil {
		return nil, nil, ErrMemberNotFound
	}
	id, err := tx.IdentityByEmail(ctx, canonical)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	mem, err := tx.MembershipByIdentityTenant(ctx, id.ID, tenant)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return id, mem, nil
}

// TenantRoles lists the roles the caller's tenant can assign: the operator's tenant-scope
// catalogue roles and the tenant's own.
func (s *Service) TenantRoles(ctx context.Context) ([]iam.Role, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	return s.iam.AssignableRoles(ctx, caller.Tenant)
}

// Invitations returns one page of the caller's tenant's invitations, newest first, each
// with the email it was issued to.
func (s *Service) Invitations(ctx context.Context, pag rdb.Pagination) ([]InvitationView, rdb.SearchResultsPagination, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, rdb.SearchResultsPagination{}, err
	}
	rows, p, err := s.iam.TenantInvitations(ctx, caller.Tenant, pag)
	if err != nil {
		return nil, p, err
	}
	out := make([]InvitationView, 0, len(rows))
	for _, r := range rows {
		email, err := s.iam.EmailOfMembership(ctx, r.MembershipID)
		if err != nil {
			return nil, p, err
		}
		out = append(out, InvitationView{Invitation: r, Email: email})
	}
	return out, p, nil
}

// InvitationView is an invitation with the email it was issued to.
type InvitationView struct {
	Invitation iam.Invitation
	Email      string
}

// ---- changing members -----------------------------------------------------------------------

// memberChange runs fn for one member of the caller's tenant under the tenant lock, after
// the checks every change shares: the tenant is live, the caller is resolved, the target
// exists here, is not the caller, is not managed by an identity provider, and holds
// nothing the caller does not. fn returns the membership's state after the change (nil
// when it removes it); the last-administrator rule is then decided over the whole tenant.
//
// 🔴 No change here touches the person's session epoch, narrowing included (ADR-081
// decision 8, as amended 2026-10-16). Every token exchange re-reads the membership and
// mints from it, so a narrowing takes effect at the person's next refresh. Rotating the
// epoch would add no revocation on top of that, and because the epoch is per IDENTITY it
// would sign the person out of every OTHER tenant they belong to, letting this tenant's
// administrator do that to them at will.
func (s *Service) memberChange(ctx context.Context, caller TenantCaller, email string,
	fn func(tx *iam.Store, a actor, id *iam.Identity, mem *iam.Membership) (*iam.Membership, error)) error {
	return s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		id, mem, err := loadTenantMember(ctx, tx, caller.Tenant, email)
		if err != nil {
			return err
		}
		if id.ID == a.identityID {
			return refuse(CodeSelfModification, "you cannot change your own membership")
		}
		if mem.ManagedBy != nil {
			return refuse(CodeManagedByIdp, "this membership is managed by an identity provider and cannot be changed here")
		}
		if err := s.requireAbove(a, mem); err != nil {
			return err
		}
		before, err := tx.TenantMemberships(ctx, caller.Tenant)
		if err != nil {
			return err
		}
		after, err := fn(tx, a, id, mem)
		if err != nil {
			return err
		}
		return requireAnAdminRemains(before, map[uint]*iam.Membership{mem.ID: after})
	})
}

// lockLiveTenant takes the tenant lock and refuses a tenant that is being deleted.
func lockLiveTenant(ctx context.Context, tx *iam.Store, tenant string) error {
	t, err := tx.LockTenant(ctx, tenant)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTenantNotFound
	}
	if err != nil {
		return err
	}
	if t.PurgeState.Deleted() {
		return ErrTenantDeleted
	}
	return nil
}

// SetMemberRoles replaces a member's roles in the caller's tenant.
func (s *Service) SetMemberRoles(ctx context.Context, email string, roleTokens []string) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	err = s.memberChange(ctx, caller, email, func(tx *iam.Store, a actor, id *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		roles, err := tx.AssignableRolesByTokens(ctx, caller.Tenant, roleTokens)
		if err != nil {
			return nil, err
		}
		if err := requireAssignable(a, roles); err != nil {
			return nil, err
		}
		if err := tx.ReplaceMembershipRoles(ctx, mem, roles); err != nil {
			return nil, err
		}
		next := *mem
		next.TenantRoles = roles
		return &next, nil
	})
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

// SetMemberScope replaces a member's scope in the caller's tenant. An empty scope is the
// whole tenant.
func (s *Service) SetMemberScope(ctx context.Context, email string, anchors []ScopeAnchorInput) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := NormalizeScope(anchors)
	if err != nil {
		return nil, err
	}
	err = s.memberChange(ctx, caller, email, func(tx *iam.Store, _ actor, _ *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		if err := tx.SetMembershipScope(ctx, mem, scope); err != nil {
			return nil, err
		}
		return mem, nil
	})
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

// SetMemberAccess replaces a member's roles and scope together in the caller's tenant, in
// one transaction: both land or neither does. The two single-purpose changes above are each
// atomic but not together, so a console that saved roles and then scope could leave the roles
// changed when the scope was refused. Every check either change makes is made here, once
// over the combined result, and the last-administrator rule is decided on the membership as
// it will stand with both applied.
func (s *Service) SetMemberAccess(ctx context.Context, email string, roleTokens []string, anchors []ScopeAnchorInput) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := NormalizeScope(anchors)
	if err != nil {
		return nil, err
	}
	err = s.memberChange(ctx, caller, email, func(tx *iam.Store, a actor, _ *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		roles, err := tx.AssignableRolesByTokens(ctx, caller.Tenant, roleTokens)
		if err != nil {
			return nil, err
		}
		if err := requireAssignable(a, roles); err != nil {
			return nil, err
		}
		if err := tx.ReplaceMembershipRoles(ctx, mem, roles); err != nil {
			return nil, err
		}
		if err := tx.SetMembershipScope(ctx, mem, scope); err != nil {
			return nil, err
		}
		next := *mem
		next.TenantRoles = roles
		return &next, nil
	})
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

// SetMemberEnabled enables or disables a member of the caller's tenant.
func (s *Service) SetMemberEnabled(ctx context.Context, email string, enabled bool) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	err = s.memberChange(ctx, caller, email, func(tx *iam.Store, _ actor, _ *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		if enabled && !mem.Enabled {
			invs, err := tx.InvitationsForMemberships(ctx, []uint{mem.ID})
			if err != nil {
				return nil, err
			}
			if hasOpenInvitation(invs[mem.ID]) {
				return nil, ErrInvitationPending
			}
		}
		if err := tx.SetMembershipEnabled(ctx, mem, enabled); err != nil {
			return nil, err
		}
		next := *mem
		next.Enabled = enabled
		return &next, nil
	})
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

// RemoveMember removes a member from the caller's tenant: the membership and its
// invitations, never the identity.
func (s *Service) RemoveMember(ctx context.Context, email string) (bool, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return false, err
	}
	err = s.memberChange(ctx, caller, email, func(tx *iam.Store, _ actor, _ *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		if err := tx.RemoveMembership(ctx, mem); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// ---- invitations ---------------------------------------------------------------------------

// InviteInput is a tenant-plane invitation.
type InviteInput struct {
	Email      string
	RoleTokens []string
	Scope      []ScopeAnchorInput
}

// newInvitationSecret returns a fresh link secret and the digest the row keeps.
func newInvitationSecret() (code string, digest []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	code = base64.RawURLEncoding.EncodeToString(raw)
	return code, InvitationDigest(code), nil
}

// InvitationDigest is the digest an invitation row keeps for a link secret. A plain
// SHA-256 is enough: the secret is 256 random bits, so there is nothing to guess and no
// key would add anything.
func InvitationDigest(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// InviteMember brings a person into the caller's tenant: a DISABLED membership with the
// given roles and scope — and, when no identity has the email yet, a pending identity with
// no password — plus a one-time invitation code, valid for InvitationTTL. The membership
// is enabled when the code is accepted.
//
// 🔴 THE ANSWER IS THE SAME WHETHER OR NOT THE PERSON ALREADY HAD AN ACCOUNT. The member
// shown carries no name while the invitation is open (see the resolver), and nothing else
// in the answer depends on the identity having existed. The one refusal that does depend on
// the store — CONFLICT for someone who is already a member — is about THIS tenant, which
// the caller may list anyway.
func (s *Service) InviteMember(ctx context.Context, in InviteInput) (*MemberView, string, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, "", err
	}
	email, err := iam.CanonicalEmail(in.Email)
	if err != nil {
		return nil, "", integrity.NewRefusal(integrity.ClassInvalid, err.Error())
	}
	scope, err := NormalizeScope(in.Scope)
	if err != nil {
		return nil, "", err
	}
	code, digest, err := newInvitationSecret()
	if err != nil {
		return nil, "", err
	}
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		roles, err := tx.AssignableRolesByTokens(ctx, caller.Tenant, in.RoleTokens)
		if err != nil {
			return err
		}
		if err := requireAssignable(a, roles); err != nil {
			return err
		}
		id, err := tx.IdentityByEmail(ctx, email)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			id = &iam.Identity{Email: email, Enabled: true}
			if err := tx.CreateIdentity(ctx, id); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.MembershipByIdentityTenant(ctx, id.ID, caller.Tenant); err == nil {
				return ErrAlreadyMember
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		mem, err := tx.AddScopedMembership(ctx, id.ID, caller.Tenant, roles, scope, false)
		if err != nil {
			return err
		}
		inviter := a.identityID
		return tx.CreateInvitation(ctx, &iam.Invitation{
			TenantId: caller.Tenant, MembershipID: mem.ID, TokenDigest: digest,
			InvitedBy: &inviter, ExpiresAt: time.Now().UTC().Add(InvitationTTL),
		})
	})
	if err != nil {
		return nil, "", err
	}
	view, err := s.memberIn(ctx, s.iam, caller.Tenant, email)
	if err != nil {
		return nil, "", err
	}
	return view, code, nil
}

// invitationChange runs fn for a member of the caller's tenant who has not joined, under
// the same checks as a membership change (memberChange) — an invitation is a membership
// that has not been accepted yet, so acting on it is acting on that member.
func (s *Service) invitationChange(ctx context.Context, caller TenantCaller, email string,
	fn func(tx *iam.Store, mem *iam.Membership, invs []iam.Invitation) error) error {
	return s.memberChange(ctx, caller, email, func(tx *iam.Store, _ actor, _ *iam.Identity, mem *iam.Membership) (*iam.Membership, error) {
		invs, err := tx.InvitationsForMemberships(ctx, []uint{mem.ID})
		if err != nil {
			return nil, err
		}
		accepted := false
		for _, i := range invs[mem.ID] {
			if i.AcceptedAt != nil {
				accepted = true
			}
		}
		if len(invs[mem.ID]) == 0 || accepted {
			return nil, rdb.NewNotFound("no invitation for this member is waiting to be accepted")
		}
		return mem, fn(tx, mem, invs[mem.ID])
	})
}

// RevokeInvitation revokes a member's open invitation, so its link stops working. The
// membership stays, disabled.
func (s *Service) RevokeInvitation(ctx context.Context, email string) (*MemberView, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	err = s.invitationChange(ctx, caller, email, func(tx *iam.Store, mem *iam.Membership, invs []iam.Invitation) error {
		n, err := tx.RevokeOpenInvitations(ctx, mem.ID, time.Now().UTC())
		if err != nil {
			return err
		}
		if n == 0 {
			return rdb.NewNotFound("no invitation for this member is waiting to be accepted")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.memberIn(ctx, s.iam, caller.Tenant, email)
}

// ReissueInvitation revokes a member's open invitation, if any, and issues a new code.
func (s *Service) ReissueInvitation(ctx context.Context, email string) (*MemberView, string, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, "", err
	}
	code, digest, err := newInvitationSecret()
	if err != nil {
		return nil, "", err
	}
	err = s.invitationChange(ctx, caller, email, func(tx *iam.Store, mem *iam.Membership, _ []iam.Invitation) error {
		now := time.Now().UTC()
		if _, err := tx.RevokeOpenInvitations(ctx, mem.ID, now); err != nil {
			return err
		}
		inviter, err := tx.IdentityByEmail(ctx, caller.Email)
		if err != nil {
			return err
		}
		return tx.CreateInvitation(ctx, &iam.Invitation{
			TenantId: caller.Tenant, MembershipID: mem.ID, TokenDigest: digest,
			InvitedBy: &inviter.ID, ExpiresAt: now.Add(InvitationTTL),
		})
	})
	if err != nil {
		return nil, "", err
	}
	view, err := s.memberIn(ctx, s.iam, caller.Tenant, email)
	if err != nil {
		return nil, "", err
	}
	return view, code, nil
}

// ---- tenant-owned roles ------------------------------------------------------------------

// TenantRoleInput is a role a tenant defines for itself.
type TenantRoleInput struct {
	Token       string
	Name        string
	Description string
	Authorities []string
}

// requireDefinable refuses a role authority set the caller may not define: an unknown
// authority (INVALID_VALUE), a system-tier one, or one the caller does not hold
// (ESCALATION).
func requireDefinable(a actor, authorities []string) error {
	for _, x := range authorities {
		if !auth.ValidAuthority(x) {
			return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("unknown authority %q", x))
		}
		if auth.Authority(x) == auth.AuthorityAll {
			continue
		}
		tiers, known := auth.TiersOf(auth.Authority(x))
		if !known || !tiers.Has(auth.TierTenant) {
			return refuse(CodeEscalation, "authority %q is not a tenant authority and cannot be granted by a tenant role", x)
		}
	}
	if missing := a.held.missingFrom(authorities); missing != "" {
		return refuse(CodeEscalation, "you cannot grant %q, which you do not hold", missing)
	}
	return nil
}

// CreateTenantRole defines a role owned by the caller's tenant.
//
// 🔴 THE ROW'S tenant_id IS THE CALLER'S TENANT, SET HERE AND FROM NOWHERE ELSE. A role
// written with tenant_id NULL is a CATALOGUE role — visible and assignable in every tenant
// on the instance — so a tenant-plane create that left it unset would let one tenant's
// administrator publish a role to all of them. Nothing in the request can name a tenant;
// TestCreateTenantRoleStampsTheTenant pins the stamp by value.
func (s *Service) CreateTenantRole(ctx context.Context, in TenantRoleInput) (*iam.Role, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := core.ValidateToken(in.Token); err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("role token %q: %v", in.Token, err))
	}
	if strings.TrimSpace(caller.Tenant) == "" {
		return nil, core.ErrNoTenant
	}
	var created *iam.Role
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		if err := requireDefinable(a, in.Authorities); err != nil {
			return err
		}
		taken, err := tx.CatalogueRoleTokenTaken(ctx, in.Token)
		if err != nil {
			return err
		}
		if taken {
			return ErrRoleTokenTaken
		}
		tenant := caller.Tenant
		r := &iam.Role{
			Scope: iam.ScopeTenant, TenantID: &tenant, Token: in.Token, Authorities: in.Authorities,
			NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description)},
		}
		if err := tx.CreateRole(ctx, r); err != nil {
			return err
		}
		created, err = tx.TenantOwnedRole(ctx, caller.Tenant, in.Token)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// loadOwnedRole resolves a role the caller's tenant owns, answering READ_ONLY for a
// catalogue role of that token and NOT_FOUND for anything else.
func loadOwnedRole(ctx context.Context, tx *iam.Store, tenant, token string) (*iam.Role, error) {
	r, err := tx.TenantOwnedRole(ctx, tenant, token)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if taken, terr := tx.CatalogueRoleTokenTaken(ctx, token); terr != nil {
		return nil, terr
	} else if taken {
		return nil, refuse(CodeReadOnly, "role %q is an instance role and cannot be changed by a tenant", token)
	}
	return nil, ErrTenantRoleNotFound
}

// UpdateTenantRole applies a partial update to a role the caller's tenant owns. Removing
// an authority may not leave the tenant without an administrator, and takes effect for the
// role's holders at their next token exchange (ADR-081 decision 8 as amended; see
// memberChange).
func (s *Service) UpdateTenantRole(ctx context.Context, token string, request *RoleUpdateRequest) (*iam.Role, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	var updated *iam.Role
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		r, err := loadOwnedRole(ctx, tx, caller.Tenant, token)
		if err != nil {
			return err
		}
		authorities := request.Authorities.ApplyTo(r.Authorities)
		if err := requireDefinable(a, authorities); err != nil {
			return err
		}
		if narrows(newAuthSet(r.Authorities), newAuthSet(authorities)) {
			holders, err := tx.MembershipsHoldingRole(ctx, r.ID)
			if err != nil {
				return err
			}
			before, err := tx.TenantMemberships(ctx, caller.Tenant)
			if err != nil {
				return err
			}
			after := map[uint]*iam.Membership{}
			for i := range holders {
				next := holders[i]
				next.TenantRoles = withRole(next.TenantRoles, r.ID, authorities)
				after[next.ID] = &next
			}
			if err := requireAnAdminRemains(before, after); err != nil {
				return err
			}
		}

		r.Name = request.Name.ApplyToNullString(r.Name)
		r.Description = request.Description.ApplyToNullString(r.Description)
		r.Authorities = authorities
		if err := tx.UpdateRole(ctx, r); err != nil {
			return err
		}
		updated, err = tx.TenantOwnedRole(ctx, caller.Tenant, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// withRole returns roles with the role id's authorities replaced.
func withRole(roles []iam.Role, id uint, authorities []string) []iam.Role {
	out := make([]iam.Role, len(roles))
	copy(out, roles)
	for i := range out {
		if out[i].ID == id {
			out[i].Authorities = authorities
		}
	}
	return out
}

// DeleteTenantRole deletes a role the caller's tenant owns. Refused while any member or
// service account holds it. Returns false when there was no such role.
func (s *Service) DeleteTenantRole(ctx context.Context, token string) (bool, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return false, err
	}
	deleted := false
	err = s.iam.InTransaction(ctx, func(tx *iam.Store) error {
		if err := lockLiveTenant(ctx, tx, caller.Tenant); err != nil {
			return err
		}
		a, err := s.resolveActor(ctx, tx, caller)
		if err != nil {
			return err
		}
		r, err := loadOwnedRole(ctx, tx, caller.Tenant, token)
		if errors.Is(err, ErrTenantRoleNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// Deleting a role is defining the absence of one: the caller must hold what it
		// grants, as it must to edit it.
		if missing := a.held.missingFrom(r.Authorities); missing != "" {
			return refuse(CodeEscalation, "you cannot delete a role granting %q, which you do not hold", missing)
		}
		holders, err := tx.MembershipsHoldingRole(ctx, r.ID)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			return fmt.Errorf("%w (%d member(s) hold %q)", ErrRoleInUse, len(holders), token)
		}
		accounts, err := tx.ServiceAccountsHoldingRole(ctx, r.ID)
		if err != nil {
			return err
		}
		if accounts > 0 {
			return fmt.Errorf("%w (%d service account(s) hold %q)", ErrRoleInUse, accounts, token)
		}
		if err := tx.DeleteRole(ctx, r); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// ErrRoleTokenTaken refuses a role at a token the other side already uses: a tenant role
// at an instance role's token, or an instance role at a token some tenant already uses for
// its own. A member assigned "operator" must mean exactly one role.
var ErrRoleTokenTaken error = conflict.New("a role with this token already exists")
