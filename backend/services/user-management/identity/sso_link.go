// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"net/url"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Linking an external account to an identity. EXPLICIT ONLY.
//
// The only way a sign-in reaches an identity is a row in iam_identity_links that a person
// made on purpose: a superuser (linkIdentity, or approving a request), or the identity's own
// owner from a password sign-in (beginSsoLink). Nothing matches an email address to an
// existing identity and links it, because an email claim is only as trustworthy as the
// provider's habit of letting its own users edit it. A sign-in that arrives with no link,
// for an address that already has an identity, leaves a REQUEST for a superuser instead and
// is refused.

// SsoOutcomeLinked is the outcome of a completed user-initiated link.
const SsoOutcomeLinked = "linked"

var (
	// ErrSsoLinkNeedsPassword refuses a link begun from a session that is itself a single
	// sign-on. Linking is how a password-holder adds a second way in; a session that came in
	// through a provider has nothing to prove it is the identity's owner but that provider.
	ErrSsoLinkNeedsPassword = errors.New("sign in with your password to link an account")
	// ErrSsoAlreadyLinked refuses a second link for an identity on the same connection.
	ErrSsoAlreadyLinked = errors.New("this account is already linked to that identity provider")
)

// resolveSsoIdentity applies rule C to a login-kind sign-in. It returns the identity to sign
// in, or a refusal outcome, or an error for a store failure. Exactly one of the three is set.
func (m *Manager) resolveSsoIdentity(ctx context.Context, conn *iam.IdpConnection, c *ssoClaims) (*iam.Identity, string, error) {
	link, err := m.iam.LinkBySubject(ctx, conn.ID, c.Subject)
	switch {
	case err == nil:
		id, err := m.iam.IdentityByID(ctx, link.IdentityID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, SsoOutcomeRefusedLinkRequired, nil
			}
			return nil, "", err
		}
		// C.1. A tenant connection never reaches an identity that holds a system role --
		// including one that was linked BEFORE the role was granted.
		if conn.Kind == iam.IdpKindTenant && len(id.SystemRoles) > 0 {
			return nil, SsoOutcomeRefusedSystemRole, nil
		}
		if !id.Enabled {
			return nil, SsoOutcomeRefusedIdentityDisabled, nil
		}
		return id, "", nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, "", err
	}

	// C.2: no link. The provider must vouch for the address, and the address must be in a
	// domain a superuser asserted for THIS connection.
	if outcome, err := m.checkAddressForConnection(ctx, conn, c); outcome != "" || err != nil {
		return nil, outcome, err
	}

	// C.4: no link and NO identity with this address, on a tenant connection that provisions:
	// create the identity and its link. This is the only automatic write, and an address that
	// already has an identity never reaches it.
	if conn.Kind == iam.IdpKindTenant && conn.JitEnabled {
		_, err := m.iam.IdentityByEmail(ctx, c.Email)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			id, perr := m.provisionIdentity(ctx, conn, c)
			if perr == nil {
				return id, "", nil
			}
			// Two first sign-ins of one person can race: the loser finds the winner's
			// link (or, if another route took the address meanwhile, falls through to a
			// request below).
			if link, lerr := m.iam.LinkBySubject(ctx, conn.ID, c.Subject); lerr == nil {
				id, ierr := m.iam.IdentityByID(ctx, link.IdentityID)
				if ierr != nil {
					return nil, "", ierr
				}
				return id, "", nil
			}
			if _, ierr := m.iam.IdentityByEmail(ctx, c.Email); ierr != nil {
				return nil, "", perr
			}
		case err != nil:
			return nil, "", err
		}
	}

	// C.3: never link automatically. Leave a request for a superuser (whether or not an
	// identity with this address exists yet -- the request is how the superuser learns the
	// external subject to link) and refuse.
	if err := m.iam.UpsertLinkRequest(ctx, &iam.SsoLinkRequest{
		ConnectionID: conn.ID, Subject: c.Subject, Email: c.Email, ClaimsDigest: claimsDigest(c),
	}); err != nil {
		return nil, "", err
	}
	return nil, SsoOutcomeRefusedLinkRequired, nil
}

// checkAddressForConnection is rule C.2: the provider vouches for the address and the
// address's domain belongs to this connection.
func (m *Manager) checkAddressForConnection(ctx context.Context, conn *iam.IdpConnection, c *ssoClaims) (string, error) {
	if !c.Verified {
		return SsoOutcomeRefusedUnverifiedEmail, nil
	}
	owner, err := m.iam.IdpConnectionByDomain(ctx, c.Domain)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SsoOutcomeRefusedUnverifiedDomain, nil
		}
		return "", err
	}
	if owner.ID != conn.ID {
		return SsoOutcomeRefusedUnverifiedDomain, nil
	}
	return "", nil
}

// BeginSsoLink starts the user-initiated link of an external account to the signed-in
// identity. It needs a PASSWORD sign-in (an identity token whose session record says so), a
// tenant connection (an instance connection links only by superuser action), and an identity
// with no link on that connection yet.
func (m *Manager) BeginSsoLink(ctx context.Context, identityToken, connectionToken, returnTo, source string) (*SsoBegin, error) {
	if !m.SsoReady() {
		return nil, ErrSsoUnavailable
	}
	if !m.ssoLimiter.allow(source) {
		m.countSso("unknown", SsoOutcomeRateLimited)
		return nil, ErrSsoRateLimited
	}
	subject, err := m.IdentitySubject(identityToken)
	if err != nil {
		return nil, err
	}
	id, err := m.sessionIdentity(ctx, subject.Email, subject.SessionEpoch, subject.SessionID)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	rec, err := m.loadSession(subject.SessionID, id.Email)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if rec.Method != SessionMethodPassword {
		return nil, ErrSsoLinkNeedsPassword
	}
	if len(connectionToken) > 128 || len(returnTo) > maxSsoField {
		return nil, ErrSsoInvalidRequest
	}
	checked, err := m.checkReturnTo(returnTo)
	if err != nil {
		return nil, ErrSsoInvalidRequest
	}
	conn, err := m.iam.IdpConnectionByToken(ctx, connectionToken)
	if err != nil {
		return nil, ErrSsoUnavailable
	}
	if conn.Kind != iam.IdpKindTenant || len(id.SystemRoles) > 0 {
		return nil, ErrSsoUnavailable
	}
	links, err := m.iam.LinksForIdentity(ctx, id.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.ConnectionID == conn.ID {
			return nil, ErrSsoAlreadyLinked
		}
	}
	return m.beginWithState(ctx, conn, ssoState{
		Kind: ssoKindLink, ReturnTo: checked, LinkSession: subject.SessionID, LinkEmail: id.Email,
	})
}

// finishLink completes a link-kind state: the provider has vouched for an account, and the
// password session that began the flow is still open.
func (m *Manager) finishLink(ctx context.Context, conn *iam.IdpConnection, st ssoState, c *ssoClaims,
	returnTo string, finish func(string, string, url.Values) SsoCallbackResult) SsoCallbackResult {

	if conn.Kind != iam.IdpKindTenant {
		return finish(SsoOutcomeRefusedLinkMismatch, c.Email, nil)
	}
	// The session that began the flow must still be open, and still a password session.
	rec, err := m.loadSession(st.LinkSession, st.LinkEmail)
	if err != nil || rec.Method != SessionMethodPassword {
		return finish(SsoOutcomeRefusedLinkSession, c.Email, nil)
	}
	id, err := m.iam.IdentityByEmail(ctx, st.LinkEmail)
	if err != nil || !id.Enabled {
		return finish(SsoOutcomeRefusedLinkSession, c.Email, nil)
	}
	if len(id.SystemRoles) > 0 {
		return finish(SsoOutcomeRefusedSystemRole, c.Email, nil)
	}
	if outcome, err := m.checkAddressForConnection(ctx, conn, c); outcome != "" || err != nil {
		if err != nil {
			log.Error().Err(err).Msg("Single sign-on could not check the address for a link.")
			return finish(SsoOutcomeServerError, c.Email, nil)
		}
		return finish(outcome, c.Email, nil)
	}
	// The provider's address must be the identity's own. Without this a victim who signs in
	// at the provider through a link an attacker began would bind the victim's account to
	// the ATTACKER's identity.
	if c.Email != id.Email {
		return finish(SsoOutcomeRefusedLinkMismatch, c.Email, nil)
	}
	err = m.iam.CreateIdentityLink(ctx, &iam.IdentityLink{
		IdentityID: id.ID, ConnectionID: conn.ID, Subject: c.Subject, CreatedBy: id.Email,
	})
	switch {
	case errors.Is(err, iam.ErrSubjectLinked), errors.Is(err, iam.ErrIdentityAlreadyLinked):
		return finish(SsoOutcomeRefusedAlreadyLinked, c.Email, nil)
	case err != nil:
		log.Error().Err(err).Msg("Single sign-on could not record a link.")
		return finish(SsoOutcomeServerError, c.Email, nil)
	}
	m.recordAuth(ctx, rdb.AuditOpIdentityLinked, id.Email, "")
	m.countSso(conn.Token, SsoOutcomeLinked)
	return SsoCallbackResult{Location: withQuery(returnTo, url.Values{"linked": {"1"}}), Outcome: SsoOutcomeLinked}
}
