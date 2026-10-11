// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
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

// startSession records a new sign-in for email and returns its id.
func (m *Manager) startSession(email string) (auth.SessionID, error) {
	sid := auth.SessionID(uuid.NewString())
	if _, err := m.refreshKV.Put(sessionKey(sid), []byte(email)); err != nil {
		return "", err
	}
	return sid, nil
}

// sessionLive is the ONE check that a credential's sign-in is still open. It is called
// from sessionIdentity, which every exchange of a previously issued credential (refresh,
// tenant selection, membership read, authorization code issue and redemption, OAuth
// refresh) already goes through, so no path can skip it.
//
// A credential with no sid is refused, not treated as an older kind: this build
// issues none, and a refused one simply signs in again. A missing or deleted record is
// errSessionEnded; a store error is returned as-is so the caller can tell an outage
// (retryable) from a closed session (terminal).
func (m *Manager) sessionLive(sid auth.SessionID, email string) error {
	if sid == "" {
		return errSessionEnded
	}
	entry, err := m.refreshKV.Get(sessionKey(sid))
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return errSessionEnded
		}
		return err
	}
	// The record names its owner; a sid presented with another identity's email is not
	// this sign-in.
	if string(entry.Value()) != email {
		return errSessionEnded
	}
	return nil
}

// Logout ends the sign-in a token belongs to: it deletes the session record, which ends
// every refresh chain, OAuth grant and identity token minted from that sign-in at its
// next exchange, and it burns the presented refresh token. token is a refresh token or an
// identity token (a user who has not yet picked a tenant holds only the latter).
//
// It is idempotent: ending a sign-in that has already ended, or presenting a token that
// has already expired, succeeds. A token that is not ours, or that names no sign-in,
// is ErrInvalidToken. A store error is returned (the caller may retry) because
// reporting success for a sign-in that is still open would be the one wrong answer.
//
// Access tokens already issued are not revoked: services verify them by signature and
// expiry alone, so one runs on for at most the access-token lifetime.
func (m *Manager) Logout(ctx context.Context, token string) error {
	claims, err := m.validator.ValidateRefresh(token)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil
		}
		var idErr error
		claims, idErr = m.validator.ValidateIdentity(token)
		if idErr != nil {
			if errors.Is(idErr, jwt.ErrTokenExpired) {
				return nil
			}
			return ErrInvalidToken
		}
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
	if claims.TokenType == auth.TokenTypeRefresh {
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
