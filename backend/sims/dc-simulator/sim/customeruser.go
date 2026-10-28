// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// customerUserRole is the built-in read-only tenant role (user-management's iam.ViewerRoleToken)
// a customer user holds. A customer sees their own devices, alarms and boards and changes
// nothing, which is the whole of what the scenarios that provision one demonstrate; the
// literal is mirrored for the reason everything in this module is.
const customerUserRole = "viewer"

// scopeKindCustomer is user-management's ScopeAnchorKind.CUSTOMER: the anchor covers the
// devices assigned to that customer.
const scopeKindCustomer = "CUSTOMER"

const queryMembersByEmail = `query($criteria:MemberSearchCriteria!){` +
	`members(criteria:$criteria){results{email enabled invitationPending scope{kind token}}}}`

const mutationInviteMember = `mutation($request:InviteMemberRequest!){` +
	`inviteMember(request:$request){invitationCode member{email}}}`

const mutationAcceptInvitation = `mutation($code:String!,$password:String){` +
	`acceptInvitation(code:$code,password:$password){identityToken}}`

const mutationSetMemberScope = `mutation($email:String!,$scope:[ScopeAnchorInput!]!){` +
	`setMemberScope(email:$email,scope:$scope){email}}`

// ensureCustomerUser makes the person a member of the tenant, limited to one customer.
//
// Create-or-converge like every step of Provision: a member who is absent is invited with
// the viewer role and the customer scope and accepts at once (with the sim's own password,
// so the sim record already tells the operator how to sign in), and one who exists has its
// scope converged to the manifest's, so a re-bootstrap repairs a member someone widened.
//
// 🔴 IT NEVER WIDENS. An empty scope means the WHOLE tenant to user-management, so any path
// that could write one for a customer user turns a "sees only the annex" account into a
// tenant-wide one while every step reports success. The scope this writes is exactly one
// customer anchor, built from a token Validate has already held to the manifest's customers.
func ensureCustomerUser(ctx context.Context, rt *Runtime, u CustomerUserSpec) error {
	if u.CustomerToken == "" {
		return fmt.Errorf("customer user %q names no customer, and a member with no scope sees the whole tenant", u.Email)
	}
	if rt.SimPassword == "" {
		return fmt.Errorf("customer user %q cannot be created: the runtime carries no sim password to accept the invitation with", u.Email)
	}
	scope := []map[string]any{{"kind": scopeKindCustomer, "token": u.CustomerToken}}

	var found struct {
		Members struct {
			Results []struct {
				Email             string `json:"email"`
				Enabled           bool   `json:"enabled"`
				InvitationPending bool   `json:"invitationPending"`
				Scope             []struct {
					Kind  string `json:"kind"`
					Token string `json:"token"`
				} `json:"scope"`
			} `json:"results"`
		} `json:"members"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, queryMembersByEmail, map[string]any{
		"criteria": map[string]any{"pageNumber": 1, "pageSize": 20, "emailPrefix": u.Email},
	}, &found); err != nil {
		return fmt.Errorf("members: %w", err)
	}
	for _, m := range found.Members.Results {
		if !strings.EqualFold(m.Email, u.Email) {
			continue
		}
		if len(m.Scope) == 1 && m.Scope[0].Kind == scopeKindCustomer && m.Scope[0].Token == u.CustomerToken {
			return nil
		}
		var out struct {
			SetMemberScope struct {
				Email string `json:"email"`
			} `json:"setMemberScope"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationSetMemberScope,
			map[string]any{"email": m.Email, "scope": scope}, &out); err != nil {
			return fmt.Errorf("setMemberScope: %w", err)
		}
		log.Info().Str("email", u.Email).Str("customer", u.CustomerToken).Msg("converged customer user scope")
		return nil
	}

	var invited struct {
		InviteMember struct {
			InvitationCode string `json:"invitationCode"`
		} `json:"inviteMember"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationInviteMember, map[string]any{
		"request": map[string]any{"email": u.Email, "roleTokens": []string{customerUserRole}, "scope": scope},
	}, &invited); err != nil {
		return fmt.Errorf("inviteMember: %w", err)
	}
	if invited.InviteMember.InvitationCode == "" {
		return fmt.Errorf("inviteMember for %q returned no invitation code", u.Email)
	}
	var accepted struct {
		AcceptInvitation struct {
			IdentityToken string `json:"identityToken"`
		} `json:"acceptInvitation"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationAcceptInvitation, map[string]any{
		"code": invited.InviteMember.InvitationCode, "password": rt.SimPassword,
	}, &accepted); err != nil {
		return fmt.Errorf("acceptInvitation: %w", err)
	}
	log.Info().Str("email", u.Email).Str("customer", u.CustomerToken).Msg("created customer user")
	return nil
}
