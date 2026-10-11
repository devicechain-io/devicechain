// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// A SIGN-IN SESSION is the server-side record of one successful login. Its id (the "sid"
// claim) rides the identity token and every refresh token minted from it -- the first
// tenant, each tenant switched to, each rotation, and any OAuth grant authorized from
// it -- so deleting the record (Logout) ends every chain of that sign-in at once, while
// another sign-in of the same identity is untouched. The session epoch stays the
// per-identity switch ("sign out everywhere": password reset, disable, delete).
//
// The record lives in the refresh-token KV bucket under a key no token id can take
// (jtis are bare UUIDs), so it shares that bucket's lifetime: a sign-in lasts at most
// the refresh TTL, after which the identity signs in again.

// sessionKeyPrefix keeps session records apart from refresh jtis in the shared bucket.
const sessionKeyPrefix = "sid."

func sessionKey(sid auth.SessionID) string { return sessionKeyPrefix + string(sid) }

// Sign-in methods a session record carries.
const (
	// SessionMethodPassword is a sign-in that compared a password.
	SessionMethodPassword = "password"
	// SessionMethodSso is a sign-in vouched for by an external identity provider.
	SessionMethodSso = "sso"
)

// sessionRecord is what a sign-in's KV entry holds. Email names the owner. Method says how
// the person proved who they are, and for a single sign-on the connection it came through
// and the moment OUR callback accepted it (Anchor: never the provider's optional
// auth_time, which a provider may omit or backdate).
type sessionRecord struct {
	Email          string      `json:"email"`
	Method         string      `json:"method"`
	ConnectionID   uint        `json:"connection_id,omitempty"`
	ConnectionKind iam.IdpKind `json:"connection_kind,omitempty"`
	Anchor         time.Time   `json:"anchor"`
}

// startSession records a new password sign-in for email and returns its id.
func (m *Manager) startSession(email string) (auth.SessionID, error) {
	return m.putSession(sessionRecord{Email: email, Method: SessionMethodPassword, Anchor: time.Now().UTC()})
}

// startSsoSession records a new single sign-on for email through conn, anchored at the
// moment the callback accepted it.
func (m *Manager) startSsoSession(email string, conn *iam.IdpConnection, anchor time.Time) (auth.SessionID, error) {
	return m.putSession(sessionRecord{
		Email: email, Method: SessionMethodSso,
		ConnectionID: conn.ID, ConnectionKind: conn.Kind, Anchor: anchor.UTC(),
	})
}

func (m *Manager) putSession(rec sessionRecord) (auth.SessionID, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	sid := auth.SessionID(uuid.NewString())
	if _, err := m.refreshKV.Put(sessionKey(sid), b); err != nil {
		return "", err
	}
	return sid, nil
}

// loadSession is the ONE check that a credential's sign-in is still open, and it hands back
// the record so the admission rules can read what kind of sign-in it is. It is called from
// sessionIdentity, which every exchange of a previously issued credential (refresh, tenant
// selection, membership read, authorization code issue and redemption, OAuth refresh)
// already goes through, so no path can skip it.
//
// A credential with no sid is refused, not treated as an older kind: this build
// issues none, and a refused one simply signs in again. A missing or deleted record is
// errSessionEnded; so is one this build cannot read, since a record that cannot be
// interpreted cannot be admitted. A store error is returned as-is so the caller can tell an
// outage (retryable) from a closed session (terminal).
func (m *Manager) loadSession(sid auth.SessionID, email string) (*sessionRecord, error) {
	if sid == "" {
		return nil, errSessionEnded
	}
	entry, err := m.refreshKV.Get(sessionKey(sid))
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return nil, errSessionEnded
		}
		return nil, err
	}
	var rec sessionRecord
	if err := json.Unmarshal(entry.Value(), &rec); err != nil {
		return nil, errSessionEnded
	}
	// The record names its owner; a sid presented with another identity's email is not
	// this sign-in.
	if rec.Email != email {
		return nil, errSessionEnded
	}
	return &rec, nil
}

// sessionLive reports whether the sign-in a credential names is still open.
func (m *Manager) sessionLive(sid auth.SessionID, email string) error {
	_, err := m.loadSession(sid, email)
	return err
}

// admitTenant decides whether the sign-in sid may be turned into a session in tenant. It is
// the ONE place the sign-in's own terms are applied to a tenant grant, and every path that
// mints one calls it -- tenant selection, refresh, the authorization-code issue, and OAuth
// redemption and refresh (the last through resolveTenantGrant) -- so a rule added here
// cannot be skipped by one of them.
//
// A password sign-in is admitted wherever the identity's memberships allow; the rules below
// bind a single sign-on:
//
//   - the connection it came through must still exist and be enabled, and the identity must
//     still be linked on it, so deleting or disabling a connection, or unlinking the
//     identity, ends its sessions at their next exchange;
//   - through a TENANT connection the session reaches only the tenants that connection
//     serves, and never an identity that holds a system role. The second rule is checked
//     here as well as at sign-in because a role granted AFTER the link was made must stop
//     working at once, not at the next sign-in;
//   - through an INSTANCE connection a tenant is entered only by a superuser breaking glass.
//     The operator's identity provider authenticates the operator; it does not give
//     ordinary membership access, which is the tenant's own sign-in to give.
//
// su is whether the identity is a superuser. The denials are errTenantAccessDenied (the
// sign-in is open but may not enter this tenant) and errSessionEnded (it is over).
func (m *Manager) admitTenant(ctx context.Context, sid auth.SessionID, id *iam.Identity, tenant string, su bool) error {
	rec, err := m.loadSession(sid, id.Email)
	if err != nil {
		return err
	}
	if rec.Method != SessionMethodSso {
		return nil
	}
	conn, err := m.iam.IdpConnectionByID(ctx, rec.ConnectionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSessionEnded
		}
		return err
	}
	if !conn.Enabled {
		return errSessionEnded
	}
	// The link the sign-in rested on must still exist: unlinking an identity ends the
	// sessions it signed in with.
	linked, err := m.iam.IdentityLinkedTo(ctx, id.ID, conn.ID)
	if err != nil {
		return err
	}
	if !linked {
		return errSessionEnded
	}
	switch conn.Kind {
	case iam.IdpKindTenant:
		if len(id.SystemRoles) > 0 {
			return errSessionEnded
		}
		served, err := m.iam.ConnectionTenants(ctx, conn.ID)
		if err != nil {
			return err
		}
		for _, t := range served {
			if t == tenant {
				return nil
			}
		}
		return errTenantAccessDenied
	case iam.IdpKindInstance:
		if su {
			return nil
		}
		return errTenantAccessDenied
	}
	// A kind this build does not know is not admitted: fail closed.
	return errSessionEnded
}

// sessionMemberships is the membership list a sign-in is shown: every membership for a
// password sign-in, and for a single sign-on only the tenants admitTenant would let it
// enter (none through an instance connection, whose operator enters a tenant by breaking
// glass rather than by membership). Listing a tenant the session cannot enter would offer a
// choice that fails.
func (m *Manager) sessionMemberships(ctx context.Context, sid auth.SessionID, id *iam.Identity) ([]MembershipInfo, error) {
	rec, err := m.loadSession(sid, id.Email)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if rec.Method != SessionMethodSso {
		return membershipInfos(id.Memberships), nil
	}
	out := []MembershipInfo{}
	for _, mem := range id.Memberships {
		switch err := m.admitTenant(ctx, sid, id, mem.TenantId, false); {
		case err == nil:
			out = append(out, MembershipInfo{Tenant: mem.TenantId, Roles: roleTokens(mem.TenantRoles)})
		case errors.Is(err, errTenantAccessDenied):
		case errors.Is(err, errSessionEnded):
			return nil, ErrInvalidToken
		default:
			return nil, err
		}
	}
	return out, nil
}

// Logout ends the sign-in a token belongs to: it deletes the session record, which ends
// every refresh chain, OAuth grant and identity token minted from that sign-in at its
// next exchange, and it burns the presented refresh token. token is a refresh token or an
// identity token (a user who has not yet picked a tenant holds only the latter).
//
// Only the SIGNATURE and the token type are checked, not the expiry: an identity token
// that lapsed fifteen minutes into the sign-in still names a sign-in whose refresh chains
// and OAuth grants may be live, and signing out with it must end them. A token whose
// signature is not ours, or that names no sign-in, is ErrInvalidToken - the same answer
// as garbage.
//
// It is idempotent: ending a sign-in that has already ended (or whose record has run
// out) succeeds. A store error is returned (the caller may retry) because reporting
// success for a sign-in that is still open would be the one wrong answer.
//
// Access tokens and identity tokens already issued are not revoked: they are verified by
// signature and expiry alone (identity tokens on the admin API included), so each runs
// on for at most its own lifetime.
func (m *Manager) Logout(ctx context.Context, token string) error {
	claims, err := m.validator.ParseSignatureOnly(token)
	if err != nil {
		return ErrInvalidToken
	}
	if claims.TokenType != auth.TokenTypeRefresh && claims.TokenType != auth.TokenTypeIdentity {
		return ErrInvalidToken
	}
	if claims.SessionID == "" {
		return ErrInvalidToken
	}
	if err := m.refreshKV.Delete(sessionKey(claims.SessionID)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		log.Error().Err(err).Msg("Logout could not end the sign-in's session record.")
		return ErrLogoutUnavailable
	}
	// The presented refresh token's own jti, so the token itself is dead even to a path
	// that read it before the record was gone. Best effort: the session record is what
	// every exchange checks.
	if claims.TokenType == auth.TokenTypeRefresh && claims.ID != "" {
		if err := m.refreshKV.Delete(claims.ID); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			log.Warn().Err(err).Msg("Logout ended the session but could not burn the presented refresh token.")
		}
	}
	m.recordAuth(ctx, rdb.AuditOpLogout, claims.Username, claims.Tenant)
	return nil
}

// ErrLogoutUnavailable is Logout's failure when the session store could not be written. Its
// text carries no cause (the mutation returns it verbatim); the sign-in is still open, so
// the caller should retry.
var ErrLogoutUnavailable = errors.New("could not sign out right now; try again")
