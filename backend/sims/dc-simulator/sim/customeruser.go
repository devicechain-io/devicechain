// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/userclient"
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
	`members(criteria:$criteria){results{email enabled invitationPending roles{token} scope{kind token}}}}`

const mutationInviteMember = `mutation($request:InviteMemberRequest!){` +
	`inviteMember(request:$request){invitationCode member{email}}}`

const mutationAcceptInvitation = `mutation($code:String!,$password:String){` +
	`acceptInvitation(code:$code,password:$password){identityToken}}`

const mutationReissueInvitation = `mutation($email:String!){` +
	`reissueInvitation(email:$email){invitationCode}}`

const mutationSetMemberAccess = `mutation($email:String!,$roleTokens:[String!]!,$scope:[ScopeAnchorInput!]!){` +
	`setMemberAccess(email:$email,roleTokens:$roleTokens,scope:$scope){email}}`

const mutationSetMemberEnabled = `mutation($email:String!,$enabled:Boolean!){` +
	`setMemberEnabled(email:$email,enabled:$enabled){email}}`

const mutationAcceptInvitationAsExisting = `mutation($code:String!,$identityToken:String!){` +
	`acceptInvitation(code:$code,identityToken:$identityToken){identityToken}}`

const mutationSetMemberScope = `mutation($email:String!,$scope:[ScopeAnchorInput!]!){` +
	`setMemberScope(email:$email,scope:$scope){email}}`

// ensureCustomerUser makes the person a member of the tenant, limited to one customer.
//
// Create-or-converge like every step of Provision: a member who is absent is invited with
// the viewer role and the customer scope and accepts at once (with the sim's own password,
// so the sim record already tells the operator how to sign in), and one who exists has its
// scope, role and enabled state converged to the manifest's, so a re-bootstrap repairs a member
// someone widened, promoted or disabled. A member whose invitation is still pending (an earlier
// run invited them and failed before accepting) gets a fresh code and accepts it, because
// nothing else can ever let them sign in.
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
				Roles             []struct {
					Token string `json:"token"`
				} `json:"roles"`
				Scope []struct {
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
		var ignored map[string]any
		scopeOK := len(m.Scope) == 1 && m.Scope[0].Kind == scopeKindCustomer && m.Scope[0].Token == u.CustomerToken
		rolesOK := len(m.Roles) == 1 && m.Roles[0].Token == customerUserRole
		switch {
		case !rolesOK:
			// Replaces roles and scope together, so a refused scope cannot leave the roles changed.
			if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationSetMemberAccess,
				map[string]any{"email": m.Email, "roleTokens": []string{customerUserRole}, "scope": scope}, &ignored); err != nil {
				return fmt.Errorf("setMemberAccess: %w", err)
			}
		case !scopeOK:
			if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationSetMemberScope,
				map[string]any{"email": m.Email, "scope": scope}, &ignored); err != nil {
				return fmt.Errorf("setMemberScope: %w", err)
			}
		}
		if !scopeOK || !rolesOK {
			log.Info().Str("email", u.Email).Str("customer", u.CustomerToken).Msg("converged customer user access")
		}
		if m.InvitationPending {
			// Left half-created by an earlier run. The old code is unrecoverable (it is shown once),
			// so issue a new one and accept it.
			var re struct {
				ReissueInvitation struct {
					InvitationCode string `json:"invitationCode"`
				} `json:"reissueInvitation"`
			}
			if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationReissueInvitation,
				map[string]any{"email": m.Email}, &re); err != nil {
				return fmt.Errorf("customer user %q has a pending invitation and reissueInvitation failed (revoke it and re-run, or remove the member): %w", u.Email, err)
			}
			if re.ReissueInvitation.InvitationCode == "" {
				return fmt.Errorf("reissueInvitation for %q returned no invitation code", u.Email)
			}
			if err := acceptInvitation(ctx, rt, m.Email, re.ReissueInvitation.InvitationCode); err != nil {
				return err
			}
			log.Info().Str("email", u.Email).Msg("accepted the pending customer user invitation")
			return nil
		}
		if !m.Enabled {
			if err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationSetMemberEnabled,
				map[string]any{"email": m.Email, "enabled": true}, &ignored); err != nil {
				return fmt.Errorf("setMemberEnabled: %w", err)
			}
			log.Info().Str("email", u.Email).Msg("re-enabled customer user")
		}
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
	if err := acceptInvitation(ctx, rt, u.Email, invited.InviteMember.InvitationCode); err != nil {
		return err
	}
	log.Info().Str("email", u.Email).Str("customer", u.CustomerToken).Msg("created customer user")
	return nil
}

// acceptInvitation redeems the code. A person with no account yet accepts by choosing the sim's
// password. An account that has ever been usable (a member removed and invited again, or an earlier
// run that signed in) can only accept by proving it is theirs, so a refused password accept falls
// back to signing in with the sim's password and presenting that identity token. Both refused is
// the error of the first, which names what the platform said about the code.
func acceptInvitation(ctx context.Context, rt *Runtime, email, code string) error {
	var accepted struct {
		AcceptInvitation struct {
			IdentityToken string `json:"identityToken"`
		} `json:"acceptInvitation"`
	}
	err := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationAcceptInvitation, map[string]any{
		"code": code, "password": rt.SimPassword,
	}, &accepted)
	if err == nil {
		return nil
	}
	ia, loginErr := userclient.Login(ctx, rt.HTTPClient, rt.Endpoints.UserGraphQL, email, rt.SimPassword)
	if loginErr != nil {
		return fmt.Errorf("acceptInvitation: %w (and the account cannot sign in with the sim password to accept as an existing account: %v)", err, loginErr)
	}
	if err2 := rt.Session.Query(ctx, rt.Endpoints.UserGraphQL, mutationAcceptInvitationAsExisting, map[string]any{
		"code": code, "identityToken": ia.IdentityToken,
	}, &accepted); err2 != nil {
		return fmt.Errorf("acceptInvitation: %w (as an existing account: %v)", err, err2)
	}
	return nil
}
