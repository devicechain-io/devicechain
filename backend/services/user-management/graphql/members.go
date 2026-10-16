// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
)

// The tenant plane's delegated administration (ADR-081): a tenant's own members,
// invitations and roles. Every resolver here gates on auth.Authorize — which refuses a
// SCOPED caller before anything else — and the service takes the caller, tenant included,
// from the verified claims rather than from anything a resolver passes.

// ContextMembersKey injects the admin Service into the TENANT plane's request context, for
// the tenant's own members and roles. It is the same Service the admin plane uses; what
// differs is which of its methods each plane's resolvers call.
const ContextMembersKey = util.ContextKey("members")

func (r *SchemaResolver) getMembersService(ctx context.Context) *admin.Service {
	return ctx.Value(ContextMembersKey).(*admin.Service)
}

// ---- scope anchors --------------------------------------------------------------------

// Wire spellings of the anchor kinds.
const (
	wireAnchorCustomer = "CUSTOMER"
	wireAnchorGroup    = "GROUP"
)

// ScopeAnchorResolver resolves the ScopeAnchor type from a stored anchor ("customer:acme").
type ScopeAnchorResolver struct {
	kind, token string
}

func (r *ScopeAnchorResolver) Kind() string  { return r.kind }
func (r *ScopeAnchorResolver) Token() string { return r.token }

// scopeAnchors renders stored anchors for the wire. A stored anchor that does not parse is
// skipped rather than invented: the token mint refuses such a membership outright, so it
// grants nothing, and the list shown must not claim a bound that is not the real one.
func scopeAnchors(anchors []string) []*ScopeAnchorResolver {
	out := make([]*ScopeAnchorResolver, 0, len(anchors))
	for _, a := range anchors {
		kind, token, err := auth.ParseAnchor(a)
		if err != nil {
			continue
		}
		out = append(out, &ScopeAnchorResolver{kind: strings.ToUpper(kind), token: token})
	}
	return out
}

// scopeAnchorInput mirrors ScopeAnchorInput.
type scopeAnchorInput struct {
	Kind  string
	Token string
}

// toScopeInput maps wire anchors onto the service's input, refusing an unknown kind.
func toScopeInput(in []scopeAnchorInput) ([]admin.ScopeAnchorInput, error) {
	if err := checkListLen("scope", len(in)); err != nil {
		return nil, err
	}
	out := make([]admin.ScopeAnchorInput, 0, len(in))
	for _, a := range in {
		var kind string
		switch a.Kind {
		case wireAnchorCustomer:
			kind = auth.AnchorKindCustomer
		case wireAnchorGroup:
			kind = auth.AnchorKindGroup
		default:
			return nil, integrity.NewRefusal(integrity.ClassInvalid, "unknown scope anchor kind "+a.Kind)
		}
		out = append(out, admin.ScopeAnchorInput{Kind: kind, Token: a.Token})
	}
	return out, nil
}

// maxScopeInputLen bounds a scope list before it is walked. It is well above the 16
// anchors a scope may hold, so the refusal a caller sees for 17 is the scope's own
// LIMIT_EXCEEDED, while a list of a million is refused here without being read.
const maxScopeInputLen = 256

func checkListLen(field string, n int) error {
	if n > maxScopeInputLen {
		return limit.Exceeded(field, n, maxScopeInputLen)
	}
	return nil
}

// ---- roles ------------------------------------------------------------------------------

// TenantRoleResolver resolves the TenantRole type.
type TenantRoleResolver struct {
	M iam.Role
}

func (r *TenantRoleResolver) Token() string         { return r.M.Token }
func (r *TenantRoleResolver) Name() *string         { return util.NullStr(r.M.Name) }
func (r *TenantRoleResolver) Description() *string  { return util.NullStr(r.M.Description) }
func (r *TenantRoleResolver) Authorities() []string { return append([]string{}, r.M.Authorities...) }
func (r *TenantRoleResolver) Owner() string {
	if r.M.IsCatalogue() {
		return "INSTANCE"
	}
	return "TENANT"
}

func tenantRoles(roles []iam.Role) []*TenantRoleResolver {
	out := make([]*TenantRoleResolver, 0, len(roles))
	for i := range roles {
		out = append(out, &TenantRoleResolver{M: roles[i]})
	}
	return out
}

// ---- members ----------------------------------------------------------------------------

// MemberResolver resolves the Member type.
type MemberResolver struct {
	M admin.MemberView
}

func (r *MemberResolver) Email() string { return r.M.Identity.Email }

// FirstName and LastName are withheld while the invitation is open. 🔴 THAT IS THE
// ACCOUNT-EXISTENCE GUARD for inviteMember: a person who already had an account may have a
// name, and one the invitation just created has none, so showing it would answer "does this
// email have an account here?" to whoever sent the invitation.
func (r *MemberResolver) FirstName() *string {
	if r.M.InvitationPending {
		return nil
	}
	return util.NullStrNonEmpty(r.M.Identity.FirstName)
}

func (r *MemberResolver) LastName() *string {
	if r.M.InvitationPending {
		return nil
	}
	return util.NullStrNonEmpty(r.M.Identity.LastName)
}

func (r *MemberResolver) Enabled() bool { return r.M.Membership.Enabled }
func (r *MemberResolver) Roles() []*TenantRoleResolver {
	return tenantRoles(r.M.Membership.TenantRoles)
}
func (r *MemberResolver) Scope() []*ScopeAnchorResolver   { return scopeAnchors(r.M.Membership.Scope) }
func (r *MemberResolver) ManagedByIdentityProvider() bool { return r.M.Membership.ManagedBy != nil }
func (r *MemberResolver) InvitationPending() bool         { return r.M.InvitationPending }

// MemberSearchResultsResolver resolves MemberSearchResults.
type MemberSearchResultsResolver struct {
	members []admin.MemberView
	pag     rdb.SearchResultsPagination
}

func (r *MemberSearchResultsResolver) Results() []*MemberResolver {
	out := make([]*MemberResolver, 0, len(r.members))
	for i := range r.members {
		out = append(out, &MemberResolver{M: r.members[i]})
	}
	return out
}

func (r *MemberSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.pag}
}

type memberCriteriaInput struct {
	PageNumber  int32
	PageSize    int32
	EmailPrefix *string
	Enabled     *bool
}

// Members lists one page of this tenant's members (requires member:read).
func (r *SchemaResolver) Members(ctx context.Context, args struct {
	Criteria memberCriteriaInput
}) (*MemberSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	criteria := iam.MemberSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		Enabled:    args.Criteria.Enabled,
	}
	if args.Criteria.EmailPrefix != nil {
		if len(*args.Criteria.EmailPrefix) > 256 {
			return nil, limit.Exceeded("emailPrefix length", len(*args.Criteria.EmailPrefix), 256)
		}
		criteria.EmailPrefix = *args.Criteria.EmailPrefix
	}
	members, pag, err := r.getMembersService(ctx).Members(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &MemberSearchResultsResolver{members: members, pag: pag}, nil
}

// Member reads one member of this tenant (requires member:read). Outside the tenant it is
// NOT_FOUND, whether or not the identity exists elsewhere.
func (r *SchemaResolver) Member(ctx context.Context, args struct{ Email string }) (*MemberResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	m, err := r.getMembersService(ctx).Member(ctx, args.Email)
	if err != nil {
		return nil, err
	}
	return &MemberResolver{M: *m}, nil
}

// TenantRoles lists the roles assignable in this tenant (requires role:read).
func (r *SchemaResolver) TenantRoles(ctx context.Context) ([]*TenantRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	roles, err := r.getMembersService(ctx).TenantRoles(ctx)
	if err != nil {
		return nil, err
	}
	return tenantRoles(roles), nil
}

// TenantAuthorities lists the authorities a tenant-owned role may grant: the tenant-tier
// vocabulary and "*" (requires role:read, refused to a scoped member like every other
// read here). It is the tenant plane's view of the admin plane's authorities(scope:
// "tenant"), which a tenant admin cannot reach — that one needs the system-tier role:read
// on an identity token — and it backs the console's role checklist. What the CALLER may
// grant is narrower (their own authorities); that bound is the write path's, not this
// list's.
func (r *SchemaResolver) TenantAuthorities(ctx context.Context) ([]string, error) {
	if err := auth.Authorize(ctx, auth.RoleRead); err != nil {
		return nil, err
	}
	return auth.AuthoritiesForScope(auth.TierTenant), nil
}

// ---- invitations ------------------------------------------------------------------------

// InvitationResolver resolves the Invitation type.
type InvitationResolver struct {
	M   admin.InvitationView
	now time.Time
}

func (r *InvitationResolver) Email() string { return r.M.Email }
func (r *InvitationResolver) State() string { return string(r.M.Invitation.State(r.now)) }
func (r *InvitationResolver) CreatedAt() string {
	return r.M.Invitation.CreatedAt.UTC().Format(time.RFC3339)
}
func (r *InvitationResolver) ExpiresAt() string {
	return r.M.Invitation.ExpiresAt.UTC().Format(time.RFC3339)
}
func (r *InvitationResolver) AcceptedAt() *string { return rfc3339(r.M.Invitation.AcceptedAt) }
func (r *InvitationResolver) RevokedAt() *string  { return rfc3339(r.M.Invitation.RevokedAt) }

func rfc3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// InvitationSearchResultsResolver resolves InvitationSearchResults.
type InvitationSearchResultsResolver struct {
	rows []admin.InvitationView
	pag  rdb.SearchResultsPagination
	now  time.Time
}

func (r *InvitationSearchResultsResolver) Results() []*InvitationResolver {
	out := make([]*InvitationResolver, 0, len(r.rows))
	for i := range r.rows {
		out = append(out, &InvitationResolver{M: r.rows[i], now: r.now})
	}
	return out
}

func (r *InvitationSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.pag}
}

type invitationCriteriaInput struct {
	PageNumber int32
	PageSize   int32
}

// Invitations lists one page of this tenant's invitations (requires member:read).
func (r *SchemaResolver) Invitations(ctx context.Context, args struct {
	Criteria invitationCriteriaInput
}) (*InvitationSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	rows, pag, err := r.getMembersService(ctx).Invitations(ctx,
		rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize})
	if err != nil {
		return nil, err
	}
	return &InvitationSearchResultsResolver{rows: rows, pag: pag, now: time.Now().UTC()}, nil
}

// MemberInvitationResolver resolves MemberInvitation.
type MemberInvitationResolver struct {
	member *admin.MemberView
	code   string
}

func (r *MemberInvitationResolver) Member() *MemberResolver { return &MemberResolver{M: *r.member} }
func (r *MemberInvitationResolver) InvitationCode() string  { return r.code }

// ---- mutations --------------------------------------------------------------------------

type inviteMemberInput struct {
	Email      string
	RoleTokens []string
	Scope      *[]scopeAnchorInput
}

// InviteMember invites a person into this tenant (requires member:write).
func (r *SchemaResolver) InviteMember(ctx context.Context, args struct {
	Request inviteMemberInput
}) (*MemberInvitationResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	if err := checkList("roleTokens", args.Request.RoleTokens); err != nil {
		return nil, err
	}
	var scope []admin.ScopeAnchorInput
	if args.Request.Scope != nil {
		var err error
		if scope, err = toScopeInput(*args.Request.Scope); err != nil {
			return nil, err
		}
	}
	m, code, err := r.getMembersService(ctx).InviteMember(ctx, admin.InviteInput{
		Email: args.Request.Email, RoleTokens: args.Request.RoleTokens, Scope: scope,
	})
	if err != nil {
		return nil, err
	}
	return &MemberInvitationResolver{member: m, code: code}, nil
}

// SetMemberRoles replaces a member's roles (requires member:write).
func (r *SchemaResolver) SetMemberRoles(ctx context.Context, args struct {
	Email      string
	RoleTokens []string
}) (*MemberResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	if err := checkList("roleTokens", args.RoleTokens); err != nil {
		return nil, err
	}
	m, err := r.getMembersService(ctx).SetMemberRoles(ctx, args.Email, args.RoleTokens)
	if err != nil {
		return nil, err
	}
	return &MemberResolver{M: *m}, nil
}

// SetMemberScope replaces a member's scope (requires member:write).
func (r *SchemaResolver) SetMemberScope(ctx context.Context, args struct {
	Email string
	Scope []scopeAnchorInput
}) (*MemberResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	scope, err := toScopeInput(args.Scope)
	if err != nil {
		return nil, err
	}
	m, err := r.getMembersService(ctx).SetMemberScope(ctx, args.Email, scope)
	if err != nil {
		return nil, err
	}
	return &MemberResolver{M: *m}, nil
}

// SetMemberEnabled enables or disables a member (requires member:write).
func (r *SchemaResolver) SetMemberEnabled(ctx context.Context, args struct {
	Email   string
	Enabled bool
}) (*MemberResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	m, err := r.getMembersService(ctx).SetMemberEnabled(ctx, args.Email, args.Enabled)
	if err != nil {
		return nil, err
	}
	return &MemberResolver{M: *m}, nil
}

// RemoveMember removes a member (requires member:write).
func (r *SchemaResolver) RemoveMember(ctx context.Context, args struct{ Email string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return false, err
	}
	ok, err := r.getMembersService(ctx).RemoveMember(ctx, args.Email)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// RevokeInvitation revokes a member's open invitation (requires member:write).
func (r *SchemaResolver) RevokeInvitation(ctx context.Context, args struct{ Email string }) (*MemberResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	m, err := r.getMembersService(ctx).RevokeInvitation(ctx, args.Email)
	if err != nil {
		return nil, err
	}
	return &MemberResolver{M: *m}, nil
}

// ReissueInvitation issues a member a new invitation code (requires member:write).
func (r *SchemaResolver) ReissueInvitation(ctx context.Context, args struct{ Email string }) (*MemberInvitationResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	m, code, err := r.getMembersService(ctx).ReissueInvitation(ctx, args.Email)
	if err != nil {
		return nil, err
	}
	return &MemberInvitationResolver{member: m, code: code}, nil
}

type tenantRoleCreateInput struct {
	Token       string
	Name        *string
	Description *string
	Authorities []string
}

// CreateTenantRole defines a role for this tenant (requires role:write). The tenant the
// role belongs to is the caller's, taken from its token by the service: the request has no
// field that could name another, or none.
func (r *SchemaResolver) CreateTenantRole(ctx context.Context, args struct {
	Request tenantRoleCreateInput
}) (*TenantRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	if err := checkList("authorities", args.Request.Authorities); err != nil {
		return nil, err
	}
	role, err := r.getMembersService(ctx).CreateTenantRole(ctx, admin.TenantRoleInput{
		Token: args.Request.Token, Name: strOrEmpty(args.Request.Name),
		Description: strOrEmpty(args.Request.Description), Authorities: args.Request.Authorities,
	})
	if err != nil {
		return nil, err
	}
	return &TenantRoleResolver{M: *role}, nil
}

// UpdateTenantRole changes a role this tenant defined (requires role:write).
func (r *SchemaResolver) UpdateTenantRole(ctx context.Context, args struct {
	Token   string
	Request admin.RoleUpdateRequest
}) (*TenantRoleResolver, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return nil, err
	}
	if err := checkList("authorities", args.Request.Authorities.Value); err != nil {
		return nil, err
	}
	role, err := r.getMembersService(ctx).UpdateTenantRole(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &TenantRoleResolver{M: *role}, nil
}

// DeleteTenantRole deletes a role this tenant defined (requires role:write).
func (r *SchemaResolver) DeleteTenantRole(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.RoleWrite); err != nil {
		return false, err
	}
	ok, err := r.getMembersService(ctx).DeleteTenantRole(ctx, args.Token)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// AcceptInvitation redeems an invitation code. Public: the code, and the password or
// identity token beside it, are the proof.
func (r *SchemaResolver) AcceptInvitation(ctx context.Context, args struct {
	Code          string
	Password      *string
	IdentityToken *string
}) (*IdentityAuthResolver, error) {
	res, err := r.getIdentityManager(ctx).AcceptInvitation(ctx, args.Code, args.Password, args.IdentityToken)
	if err != nil {
		return nil, err
	}
	return &IdentityAuthResolver{auth: res}, nil
}
