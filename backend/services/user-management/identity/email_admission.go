// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-user-management/iam"
)

// An identity whose STORED email is not in the one canonical form (iam.CanonicalEmail) was
// written before that form existed, or around the store's guard. Nothing that looks an
// address up can reach it any more -- every lookup is by the canonical form -- but a
// credential minted for it, or a single sign-on link that resolves it by subject, still
// names it. It authenticates nowhere: no sign-in starts a session for it and no existing
// session of it is exchanged. An administrator can still find it (iam.Store.IdentityForAdmin)
// and delete or recreate it.

// CodeIdentityEmailNotCanonical is the GraphQL extension code of ErrIdentityEmailNotCanonical.
const CodeIdentityEmailNotCanonical = "IDENTITY_EMAIL_NOT_CANONICAL"

// IdentityEmailNotCanonicalError is the typed refusal of an identity whose stored email is
// not in canonical form. It is returned only to a caller that has already authenticated as
// that identity (a valid credential for it, or its identity provider's assertion), so it
// tells that person something about their own account: an administrator has to recreate it.
type IdentityEmailNotCanonicalError struct{}

func (IdentityEmailNotCanonicalError) Error() string {
	return "this identity's stored email is not in canonical form, so it cannot sign in; ask an administrator to recreate it"
}

// Extensions makes the refusal machine-readable: graphql-go copies it onto `extensions`.
func (IdentityEmailNotCanonicalError) Extensions() map[string]any {
	return map[string]any{"code": CodeIdentityEmailNotCanonical}
}

// ErrIdentityEmailNotCanonical is the one IdentityEmailNotCanonicalError value.
var ErrIdentityEmailNotCanonical error = IdentityEmailNotCanonicalError{}

// errEmailNotCanonical is sessionIdentity's refusal. It IS an ended session (errors.Is
// errSessionEnded), so every exchange that already ends a session on errSessionEnded --
// burning a refresh token, answering invalid_grant -- ends this one the same way; the
// callers that speak GraphQL name it with sessionRefusal.
var errEmailNotCanonical = fmt.Errorf("%w: the identity's stored email is not canonical", errSessionEnded)

// admitStoredEmail refuses an identity whose stored email is not canonical. It is the ONE
// check, called wherever an identity is authenticated: sessionIdentity (every exchange of an
// existing credential), password sign-in and single sign-on completion. The log names the
// identity by id, never by its address.
func admitStoredEmail(id *iam.Identity) error {
	if c, err := iam.CanonicalEmail(id.Email); err == nil && c == id.Email {
		return nil
	}
	log.Warn().Uint("identity_id", id.ID).
		Msg("Refused to authenticate an identity whose stored email is not in canonical form; an administrator must recreate it.")
	return errEmailNotCanonical
}

// sessionRefusal maps an ended-session error from sessionIdentity onto what a GraphQL
// caller is told: the typed refusal for a non-canonical identity, otherwise ErrInvalidToken.
func sessionRefusal(err error) error {
	if errors.Is(err, errEmailNotCanonical) {
		return ErrIdentityEmailNotCanonical
	}
	return ErrInvalidToken
}
