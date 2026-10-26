// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// A sign-in record is keyed by its sid, which a token carries but an administrator does not
// know. "Sign this person out everywhere" starts from an email, so each sign-in also writes
// an INDEX entry, sidx.<hash of the email>.<sid>, and revoking lists the entries under the
// email's prefix with a filtered watch. One key per (identity, sign-in) rather than one
// list per identity on purpose: a list needs a read-modify-write that two concurrent
// sign-ins would race on, while a key per sign-in needs only a put, and the bucket's own
// expiry removes it with the record it points at. The hash keeps the email, which is not a
// legal key, out of the key space.
const sessionIndexPrefix = "sidx."

func sessionIndexPattern(email string) string { return sessionIndexPrefix + emailKey(email) + ".*" }

func sessionIndexKey(email string, sid auth.SessionID) string {
	return sessionIndexPrefix + emailKey(email) + "." + string(sid)
}

func emailKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])
}

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
	// The index is written AFTER the record and the sign-in is refused if it cannot be: a
	// session that "sign out everywhere" could not find would be one it could not end.
	if _, err := m.refreshKV.Put(sessionIndexKey(rec.Email, sid), []byte(sid)); err != nil {
		if derr := m.refreshKV.Delete(sessionKey(sid)); derr != nil && !errors.Is(derr, nats.ErrKeyNotFound) {
			log.Warn().Err(derr).Msg("A sign-in whose index entry could not be written left its record behind; it expires with the bucket.")
		}
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
// A PASSWORD sign-in is admitted wherever the identity's memberships allow, except into a
// tenant that requires single sign-on (Tenant.SsoEnforced): there it is refused, unless the
// identity is a superuser, who is let through so an identity-provider outage cannot lock the
// operator out, and the entry is audited. This is also what keeps the MCP authorize page's
// password form from reaching an enforced tenant, since consent mints its code through here.
//
// The rules below bind a single sign-on (admitSsoSession):
//
//   - the connection it came through must still exist and be enabled, and the identity must
//     still be linked on it, so deleting or disabling a connection, or unlinking the
//     identity, ends its sessions at their next exchange;
//   - the sign-in must be younger than the connection's session ceiling, counted from the
//     moment OUR callback accepted it and never extended by a refresh;
//   - through a TENANT connection the session reaches only the tenants that connection
//     serves, and never an identity that holds a system role. The second rule is checked
//     here as well as at sign-in because a role granted AFTER the link was made must stop
//     working at once, not at the next sign-in;
//   - through an INSTANCE connection a tenant is entered only by a superuser breaking glass,
//     and each entry is audited with the connection. The operator's identity provider
//     authenticates the operator; it does not give ordinary membership access, which is the
//     tenant's own sign-in to give.
//
// su is whether the identity is a superuser. The denials are errTenantAccessDenied (the
// sign-in is open but may not enter this tenant) and errSessionEnded (it is over).
func (m *Manager) admitTenant(ctx context.Context, sid auth.SessionID, id *iam.Identity, tenant string, su bool) error {
	rec, err := m.loadSession(sid, id.Email)
	if err != nil {
		return err
	}
	if rec.Method != SessionMethodSso {
		return m.admitPasswordSession(ctx, id, tenant, su)
	}
	conn, err := m.admitSsoSession(ctx, rec, id)
	if err != nil {
		return err
	}
	switch conn.Kind {
	case iam.IdpKindTenant:
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
			m.recordAuthLabeled(ctx, rdb.AuditOpSsoBreakGlass, id.Email, tenant, conn.Token)
			return nil
		}
		return errTenantAccessDenied
	}
	// A kind this build does not know is not admitted: fail closed.
	return errSessionEnded
}

// admitPasswordSession applies the tenant's own sign-in requirement to a password sign-in.
func (m *Manager) admitPasswordSession(ctx context.Context, id *iam.Identity, tenant string, su bool) error {
	t, err := m.iam.TenantByToken(ctx, tenant)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// No such tenant: nothing to enforce here. The grant itself refuses a member (and
			// admits a superuser, as it always has).
			return nil
		}
		return err
	}
	if !t.SsoEnforced {
		return nil
	}
	if su {
		m.recordAuthLabeled(ctx, rdb.AuditOpSsoEnforcementBypassed, id.Email, tenant, "")
		return nil
	}
	return errSsoRequired
}

// errSsoRequired is admitPasswordSession's refusal. It IS an errTenantAccessDenied, so every
// grant point that does not name it (OAuth redemption and refresh, the authorize step)
// treats it exactly as before; SelectTenant and Refresh turn it into ErrSsoRequired.
var errSsoRequired = fmt.Errorf("%w: the tenant requires single sign-on", errTenantAccessDenied)

// CodeSsoRequired is the GraphQL extension code of ErrSsoRequired.
const CodeSsoRequired = "SSO_REQUIRED"

// SsoRequiredError is the typed refusal of a password session in a tenant that requires
// single sign-on, so a client can send the person to their identity provider instead of
// reporting a wrong password or an expired session.
//
// It is only ever returned to a caller that has ALREADY proved who it is (a valid identity
// or refresh token) and is a member of the tenant: SelectTenant and Refresh refuse a
// non-member before admitTenant is reached. So it tells a member something about their own
// tenant, which the membership list already shows (MembershipInfo.SsoRequired), and never
// tells an unauthenticated caller or an outsider whether a tenant enforces single sign-on.
type SsoRequiredError struct{}

func (SsoRequiredError) Error() string {
	return "this tenant requires single sign-on; sign in through your identity provider"
}

// Extensions makes the refusal machine-readable: graphql-go copies it onto `extensions`.
func (SsoRequiredError) Extensions() map[string]any {
	return map[string]any{"code": CodeSsoRequired}
}

// ErrSsoRequired is the one SsoRequiredError value (errors.Is matches it).
var ErrSsoRequired error = SsoRequiredError{}

// admitSsoSession applies the rules that bind a single sign-on whatever tenant it is headed
// for, and returns the connection it came through. It is shared by admitTenant and the
// membership listing, so a sign-in that has ended is refused by both even when the
// identity has no membership for the per-tenant rules to look at.
func (m *Manager) admitSsoSession(ctx context.Context, rec *sessionRecord, id *iam.Identity) (*iam.IdpConnection, error) {
	conn, err := m.iam.IdpConnectionByID(ctx, rec.ConnectionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSessionEnded
		}
		return nil, err
	}
	if !conn.Enabled {
		return nil, errSessionEnded
	}
	// The link the sign-in rested on must still exist: unlinking an identity ends the
	// sessions it signed in with.
	linked, err := m.iam.IdentityLinkedTo(ctx, id.ID, conn.ID)
	if err != nil {
		return nil, err
	}
	if !linked {
		return nil, errSessionEnded
	}
	// The ceiling is counted from OUR callback time (rec.Anchor), so a provider that omits
	// or backdates its own auth_time changes nothing, and it is never moved by a refresh:
	// the record is written once, at sign-in. A ceiling that is not positive ends the
	// session rather than lasting forever. The stored value is capped again here by the
	// same bound the API applies, so a refresh lifetime configured shorter AFTER a connection
	// was given a longer ceiling shortens its sessions instead of being outlived by them.
	maxAge := time.Duration(conn.SessionMaxAgeSeconds) * time.Second
	if limit := m.MaxSessionMaxAge(); maxAge > limit {
		maxAge = limit
	}
	if !m.clock().Before(rec.Anchor.Add(maxAge)) {
		return nil, errSessionEnded
	}
	if conn.Kind == iam.IdpKindTenant && len(id.SystemRoles) > 0 {
		return nil, errSessionEnded
	}
	return conn, nil
}

// sessionMemberships is the membership list a sign-in is shown: for a password sign-in,
// every membership, with those in a tenant that requires single sign-on marked SsoRequired
// (never for a superuser, who is let in), so the person is told to use their identity
// provider rather than finding the tenant gone; and for a single sign-on only the tenants
// admitTenant would let it enter (none through an instance connection, whose operator
// enters a tenant by breaking glass rather than by membership).
func (m *Manager) sessionMemberships(ctx context.Context, sid auth.SessionID, id *iam.Identity) ([]MembershipInfo, error) {
	rec, err := m.loadSession(sid, id.Email)
	if err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if rec.Method != SessionMethodSso {
		if isSuperuser(id) {
			return membershipInfos(id.Memberships), nil
		}
		tokens := make([]string, 0, len(id.Memberships))
		for _, mem := range id.Memberships {
			tokens = append(tokens, mem.TenantId)
		}
		enforced, err := m.iam.SsoEnforcedAmong(ctx, tokens)
		if err != nil {
			return nil, err
		}
		out := make([]MembershipInfo, 0, len(id.Memberships))
		for _, mem := range id.Memberships {
			out = append(out, MembershipInfo{
				Tenant: mem.TenantId, Roles: roleTokens(mem.TenantRoles), SsoRequired: enforced[mem.TenantId],
			})
		}
		return out, nil
	}
	if _, err := m.admitSsoSession(ctx, rec, id); err != nil {
		if errors.Is(err, errSessionEnded) {
			return nil, ErrInvalidToken
		}
		return nil, err
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

// clock is the current time, from the manager's injectable source.
func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// recordAuthLabeled is recordAuth for an event that names something besides the actor and
// the tenant. Best-effort, like recordAuth.
func (m *Manager) recordAuthLabeled(ctx context.Context, operation, actor, tenant, label string) {
	if err := m.db.RecordAuthEventLabeled(ctx, operation, actor, tenant, label); err != nil {
		log.Error().Err(err).Str("operation", operation).Msg("Failed to record auth audit event")
	}
}

// ErrIdentityNotFound is returned by a call that names an identity there is none of.
var ErrIdentityNotFound = rdb.NewNotFound("identity not found")

// RevokeSessions ends every sign-in the identity has, wherever it is: password or single
// sign-on, console, dashboard or MCP grant. It is the administrator's "sign out everywhere".
//
// Two things end the sessions, and both are needed. Rotating the identity's session epoch
// kills every refresh token, identity token and authorization code minted under the old
// one at its next exchange, and is the part that cannot be missed. Deleting the sign-in
// records (found through the per-identity index) is what ends them by name, so each is
// gone for every path that checks the record and not only those that compare the epoch.
// The password is untouched: the person can sign in again at once. It returns how many
// sign-in records it ended.
//
// Access tokens and identity tokens already issued run on until they expire (the configured
// access-token lifetime, fifteen minutes by default), because they are verified by
// signature and expiry alone -- on the admin API too. That bound is the same one a logout
// has.
//
// The audit row is written as soon as the epoch has rotated, before the records are
// deleted: from that moment every session is ended, so a failure deleting the records
// must not leave the revoke unrecorded.
func (m *Manager) RevokeSessions(ctx context.Context, email string) (int, error) {
	// An administrator action: IdentityForAdmin also finds an identity whose stored email
	// is not canonical, so its sessions can be ended.
	id, err := m.iam.IdentityForAdmin(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrIdentityNotFound
		}
		return 0, err
	}
	if err := m.iam.RotateSessionEpoch(ctx, id); err != nil {
		return 0, err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpSessionsRevoked, actor(ctx), "", id.Email)
	return m.endSessionRecords(id.Email)
}

// ErrClearPasswordNoOtherWayIn refuses removing the password of an identity that has no
// other way to sign in.
var ErrClearPasswordNoOtherWayIn = errors.New("the identity is not linked to an enabled identity provider connection it can sign in through, so removing its password would leave it no way in; link it first")

// ClearPassword removes an identity's password and ends its sessions, leaving it able to
// sign in only through single sign-on. An empty stored hash never matches a password (the
// compare runs against a dummy at the same cost as an unknown email), and the epoch
// rotates in the same write, so every session the old password opened ends.
//
// It is refused for an identity with no link it could sign in through -- an enabled
// connection, and for an identity holding a system role an INSTANCE one, since a tenant
// connection never reaches such an identity -- because the result would be an identity
// nobody can use until someone sets a password again. Like RevokeSessions, the audit row
// is written as soon as the password is gone.
func (m *Manager) ClearPassword(ctx context.Context, email string) (*iam.Identity, error) {
	// An administrator action (see RevokeSessions).
	id, err := m.iam.IdentityForAdmin(ctx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIdentityNotFound
		}
		return nil, err
	}
	ok, err := m.hasSsoWayIn(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrClearPasswordNoOtherWayIn
	}
	if err := m.iam.SetPasswordHash(ctx, id, ""); err != nil {
		return nil, err
	}
	// The second factor protects only password sign-ins, so with the password gone it protects
	// nothing: remove it, seed included, in one transaction.
	if _, err := m.iam.RemoveMfa(ctx, m.secrets, id.ID); err != nil {
		return nil, err
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpPasswordCleared, actor(ctx), "", id.Email)
	if _, err := m.endSessionRecords(id.Email); err != nil {
		return nil, err
	}
	return id, nil
}

// hasSsoWayIn reports whether the identity is linked to an enabled connection that would
// let it sign in.
func (m *Manager) hasSsoWayIn(ctx context.Context, id *iam.Identity) (bool, error) {
	links, err := m.iam.LinksForIdentity(ctx, id.ID)
	if err != nil {
		return false, err
	}
	for _, l := range links {
		conn, err := m.iam.IdpConnectionByID(ctx, l.ConnectionID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return false, err
		}
		if !conn.Enabled {
			continue
		}
		if conn.Kind == iam.IdpKindInstance || len(id.SystemRoles) == 0 {
			return true, nil
		}
	}
	return false, nil
}

// endSessionRecords deletes every sign-in record the index lists for email, and the index
// entries with them. Deleting what is already gone is not an error, so a retry after a
// partial failure finishes the job.
func (m *Manager) endSessionRecords(email string) (int, error) {
	w, err := m.refreshKV.Watch(sessionIndexPattern(email), nats.IgnoreDeletes(), nats.MetaOnly())
	if err != nil {
		return 0, err
	}
	defer func() { _ = w.Stop() }()
	var keys []string
	timeout := time.After(10 * time.Second)
collect:
	for {
		select {
		case e := <-w.Updates():
			if e == nil {
				// nil marks the end of the entries that existed when the watch began.
				break collect
			}
			keys = append(keys, e.Key())
		case <-timeout:
			return 0, errors.New("listing the identity's sessions timed out; try again")
		}
	}
	ended := 0
	prefix := sessionIndexPrefix + emailKey(email) + "."
	for _, k := range keys {
		sid := auth.SessionID(strings.TrimPrefix(k, prefix))
		// Count only a record that was still there: deleting an absent key succeeds, and a
		// revoke that reports sessions it did not end would mislead whoever reads the count.
		if _, err := m.refreshKV.Get(sessionKey(sid)); err == nil {
			ended++
		} else if !errors.Is(err, nats.ErrKeyNotFound) {
			return ended, err
		}
		if err := m.refreshKV.Delete(sessionKey(sid)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			return ended, err
		}
		if err := m.refreshKV.Delete(k); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			return ended, err
		}
	}
	return ended, nil
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
	_, err := m.endSignIn(ctx, token)
	return err
}

// endSignIn is Logout, handing back the record it ended so SignOut can tell how the person
// signed in. The record is nil when it could not be read (already ended, run out, or
// unreadable); the sign-in is ended all the same.
func (m *Manager) endSignIn(ctx context.Context, token string) (*sessionRecord, error) {
	claims, err := m.validator.ParseSignatureOnly(token)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if claims.TokenType != auth.TokenTypeRefresh && claims.TokenType != auth.TokenTypeIdentity {
		return nil, ErrInvalidToken
	}
	if claims.SessionID == "" {
		return nil, ErrInvalidToken
	}
	// The record names its owner, which is what finds the index entry that points at it.
	// Read before the delete; an unreadable record only costs the entry its early removal,
	// because it expires with the bucket.
	var owner string
	var ended *sessionRecord
	if entry, err := m.refreshKV.Get(sessionKey(claims.SessionID)); err == nil {
		var rec sessionRecord
		if json.Unmarshal(entry.Value(), &rec) == nil {
			owner = rec.Email
			ended = &rec
		}
	}
	if err := m.refreshKV.Delete(sessionKey(claims.SessionID)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		log.Error().Err(err).Msg("Logout could not end the sign-in's session record.")
		return nil, ErrLogoutUnavailable
	}
	if owner != "" {
		if err := m.refreshKV.Delete(sessionIndexKey(owner, claims.SessionID)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			log.Warn().Err(err).Msg("Logout ended the session but could not remove its index entry; it expires with the bucket.")
		}
	}
	// The presented refresh token's own jti, so the token itself is dead even to a path
	// that read it before the record was gone. Best effort: the session record is what
	// every exchange checks.
	if claims.TokenType == auth.TokenTypeRefresh && claims.ID != "" {
		if err := m.refreshKV.Delete(claims.ID); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
			log.Warn().Err(err).Msg("Logout ended the session but could not burn the presented refresh token.")
		}
	}
	// The sign-out is filed under the token's tenant only if the token belongs to that
	// tenant's current incarnation. A refresh token issued for an earlier tenant at the same
	// token (deleted, then re-created) has already ended its sign-in above, as logout must,
	// but filing it would put the earlier tenant's event in the new tenant's journal. An
	// identity token names no tenant and is filed as before.
	//
	// A store error while placing the tenant also leaves the sign-out unfiled. Filing it
	// anyway would be the same cross-tenant write whenever the token is a predecessor's,
	// and nothing can tell which it is without the row; the tenant row and the journal live
	// in one database, so the write would most likely fail too. A dropped audit row is
	// logged at the level recordAuth uses for one.
	if claims.TokenType == auth.TokenTypeRefresh {
		if err := m.admitRefreshTenant(ctx, claims); err != nil {
			if !errors.Is(err, errTenantAccessDenied) {
				log.Error().Err(err).Msg("Logout ended the sign-in but could not place its token's tenant; the sign-out is not audited.")
			}
			return ended, nil
		}
	}
	m.recordAuth(ctx, rdb.AuditOpLogout, claims.Username, claims.Tenant)
	return ended, nil
}

// ErrLogoutUnavailable is Logout's failure when the session store could not be written. Its
// text carries no cause (the mutation returns it verbatim); the sign-in is still open, so
// the caller should retry.
var ErrLogoutUnavailable = errors.New("could not sign out right now; try again")
