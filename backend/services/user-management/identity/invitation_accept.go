// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ViewerAuthorities returns the read-only baseline every enabled tenant member receives on
// top of its roles. The admin service counts it as held when it decides what a tenant
// administrator may grant (ADR-081).
func ViewerAuthorities() []string { return append([]string(nil), viewerAuthorities...) }

// ErrAcceptNeedsOneProof refuses an acceptance that gives both or neither of a password
// and an identity token: each names a different way of proving who is accepting.
var ErrAcceptNeedsOneProof = integrity.NewRefusal(integrity.ClassInvalid,
	"accept an invitation with exactly one of a password (a new account) or an identity token (an existing one)")

// AcceptInvitation redeems a one-time invitation code (ADR-081) and enables the membership
// it names. The person proves who they are in ONE of two ways:
//
//   - identityToken: an existing account signs in first (with its password, or through
//     single sign-on) and presents the identity token. It must name the invited identity.
//   - password: a NEW account — one the invitation itself created, which has no password,
//     no system role and no link to an identity provider — sets its password here. A
//     tenant that requires single sign-on refuses this (SSO_REQUIRED).
//
// 🔴 THE PASSWORD PATH IS FOR AN ACCOUNT NOBODY HAS USED, AND THAT IS THE WHOLE GUARD. The
// code is held by whoever the inviting administrator gave it to — the administrator
// included. If the password path accepted ANY identity without a password, an administrator
// could invite the address of someone who signs in only through single sign-on (no
// password, member of other tenants), redeem the code with a password of their choosing,
// and sign in as that person everywhere a password is still accepted. So an identity that
// has ever been usable — a password, a system role, a provider link — can only accept by
// signing in.
//
// Every way a code cannot be used — unknown, expired, revoked, already accepted, a tenant
// gone or disabled, an identity token naming someone else, an account that must sign in
// instead — is the one INVALID_INVITATION answer, so a code cannot be probed for which.
//
// It returns what login returns: the identity token (the one presented, or a fresh one
// from signing in with the new password) and the identity's memberships, the accepted one
// now among them.
func (m *Manager) AcceptInvitation(ctx context.Context, code string, password, identityToken *string) (*IdentityAuth, error) {
	hasPassword := password != nil && *password != ""
	hasToken := identityToken != nil && *identityToken != ""
	if hasPassword == hasToken {
		return nil, ErrAcceptNeedsOneProof
	}
	if code == "" {
		return nil, admin.ErrInvalidInvitation
	}
	// The identity token is resolved to its person BEFORE the transaction: its session
	// check reads stores the transaction does not need to hold.
	var claims *auth.Claims
	var who *iam.Identity
	if hasToken {
		c, err := m.validator.ValidateIdentity(*identityToken)
		if err != nil {
			return nil, ErrInvalidToken
		}
		claims = c
		who, err = m.sessionIdentity(ctx, claims.Email, claims.SessionEpoch, claims.SessionID)
		if err != nil {
			if errors.Is(err, errSessionEnded) {
				return nil, ErrInvalidToken
			}
			return nil, err
		}
	}

	digest := admin.InvitationDigest(code)
	var email string
	err := m.iam.InTransaction(ctx, func(tx *iam.Store) error {
		now := time.Now().UTC()
		inv, err := tx.LockInvitationByDigest(ctx, digest)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return admin.ErrInvalidInvitation
		}
		if err != nil {
			return err
		}
		if inv.State(now) != iam.InvitationPending {
			return admin.ErrInvalidInvitation
		}
		mem, err := tx.MembershipByID(ctx, inv.MembershipID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return admin.ErrInvalidInvitation
		}
		if err != nil {
			return err
		}
		tenant, err := tx.TenantByToken(ctx, inv.TenantId)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return admin.ErrInvalidInvitation
		}
		if err != nil {
			return err
		}
		if tenant.PurgeState.Deleted() || !tenant.Enabled {
			return admin.ErrInvalidInvitation
		}
		id, err := tx.IdentityByID(ctx, mem.IdentityID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return admin.ErrInvalidInvitation
		}
		if err != nil {
			return err
		}
		if !id.Enabled {
			return admin.ErrInvalidInvitation
		}

		if hasToken {
			if who.ID != id.ID {
				return admin.ErrInvalidInvitation
			}
		} else {
			pristine, err := neverUsed(ctx, tx, id, mem)
			if err != nil {
				return err
			}
			if !pristine {
				return admin.ErrInvalidInvitation
			}
			if tenant.SsoEnforced {
				return ErrSsoRequired
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			if err := tx.SetPasswordHash(ctx, id, string(hash)); err != nil {
				return err
			}
		}
		if err := tx.SetMembershipEnabled(ctx, mem, true); err != nil {
			return err
		}
		if err := tx.MarkInvitationAccepted(ctx, inv, now); err != nil {
			return err
		}
		email = id.Email
		return nil
	})
	if err != nil {
		return nil, err
	}

	if hasPassword {
		return m.Login(ctx, email, *password)
	}
	id, err := m.iam.IdentityByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	memberships, err := m.sessionMemberships(ctx, claims.SessionID, id)
	if err != nil {
		return nil, err
	}
	var expires time.Time
	if claims.ExpiresAt != nil {
		expires = claims.ExpiresAt.Time
	}
	return &IdentityAuth{
		IdentityToken: *identityToken,
		ExpiresAt:     expires,
		Superuser:     isSuperuser(id),
		Memberships:   memberships,
	}, nil
}

// neverUsed reports whether an identity has never been usable: no password, no system
// role, no link to an identity provider, and no membership but pending invitations — the
// state an invitation creates an identity in, and the only one the password path of
// AcceptInvitation may set a password on.
//
// 🔴 THE MEMBERSHIP CHECK IS NOT REDUNDANT WITH THE OTHER THREE. An identity can lose its
// way in and keep its grants: deleting an identity provider connection removes its links
// and leaves the memberships it did not manage, enabled; a tenant can enable a member
// whose invitation it revoked. Such an identity was usable, and a password set on it by
// whoever holds a NEW tenant's invitation code would enter every tenant it belongs to.
// Other memberships are allowed only while they are disabled and waiting on an
// invitation of their own — two tenants inviting the same new person — which is the one
// state in which the identity has provably never had a session anywhere.
func neverUsed(ctx context.Context, tx *iam.Store, id *iam.Identity, accepting *iam.Membership) (bool, error) {
	if id.PasswordHash != "" || len(id.SystemRoles) > 0 {
		return false, nil
	}
	// A second factor is proof the identity has had a session: it can only be enrolled from one.
	hasFactor, err := tx.HasMfaFactor(ctx, id.ID)
	if err != nil {
		return false, err
	}
	if hasFactor {
		return false, nil
	}
	links, err := tx.LinksForIdentity(ctx, id.ID)
	if err != nil {
		return false, err
	}
	if len(links) > 0 {
		return false, nil
	}
	others := make([]uint, 0, len(id.Memberships))
	for _, m := range id.Memberships {
		if m.ID == accepting.ID {
			continue
		}
		if m.Enabled {
			return false, nil
		}
		others = append(others, m.ID)
	}
	invs, err := tx.InvitationsForMemberships(ctx, others)
	if err != nil {
		return false, err
	}
	for _, mid := range others {
		open := false
		for _, inv := range invs[mid] {
			if inv.AcceptedAt == nil && inv.RevokedAt == nil {
				open = true
			}
		}
		if !open {
			return false, nil
		}
	}
	return true, nil
}
