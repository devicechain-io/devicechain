// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"strings"
)

// WHICH RECORD AN UPDATE WRITES, for the mutations that still share their create
// input.
//
// Every update* on the platform declares `token: String!`. A mutation that has been
// converted to a dedicated *UpdateRequest needs nothing here — its input carries no
// token, so naming a second record is unrepresentable. The ones still reusing a
// *CreateRequest carry the token TWICE, once to say which record and once inside the
// shared input, and those two can disagree. These two functions are the only
// sanctioned answers to that.
//
// # The defect they replace
//
// The disagreement used to resolve differently in every area, and never safely:
//
//   - Most updates located the row by the PAYLOAD token and ignored the argument
//     outright, so a request naming one record in `token:` and another in
//     `request.token` silently updated the second and returned it with a success.
//   - The rest honoured the argument and then wrote the payload token over the
//     stored one. An empty payload token — which `token: String!` permits, since ""
//     is a perfectly good non-null String — therefore BLANKED the record's token.
//     The row survived, addressable by nothing, and the mutation returned success.
//
// The second one is the reason these are two functions rather than one flag. Both
// policies below refuse a blank payload token; they differ only on whether a
// DIFFERENT one is a rename or a mistake.

// ErrPayloadTokenDisagrees is the rule for an update whose payload token is NOT a
// rename channel: the `token` argument names the record, the payload token may only
// agree with it, and a disagreement is refused rather than applied to whichever
// record the payload happened to name.
//
// An EMPTY payload token is accepted as "unspecified" rather than refused. Under a
// shared create input a caller who has nothing to say about identity has no other
// way to say it, and the caller's own update must not write it — see the callers,
// which drop the `row.Token = request.Token` assignment entirely rather than
// assigning a value this function just declared meaningless.
//
// Whitespace is deliberately NOT trimmed before the comparison. A token of "  " can
// never equal a stored one, so it lands in the refusal branch, which is where a
// malformed token belongs; trimming would instead silently accept " tok " as naming
// "tok" while nothing in the system stores the trimmed form.
func ErrPayloadTokenDisagrees(entity, token, requested string) error {
	if requested == "" || requested == token {
		return nil
	}
	return fmt.Errorf("cannot update %s %q through a request naming %q: the token argument "+
		"identifies the record, and a request that disagrees with it is refused rather than "+
		"applied to whichever one the payload happened to name", entity, token, requested)
}

// ErrRenameTokenUnusable is the rule for the updates where a differing payload token
// IS meant: it names the record's NEW token. Only three mutations are in this class,
// and each earned it the same way — a test pins the rename as intended, and the
// things that depend on the record hold its immutable id rather than its token, so a
// rename cannot orphan them:
//
//   - updateDeviceProfile — additionally refused once the profile is published or
//     adopted, because published rules and dead-man rosters DO key on the token from
//     that point on. That guard lives with the mutation; this function is only the
//     blank check.
//   - updateNotificationChannel — the delivery secret is keyed by the channel's id,
//     and a policy's rules store ChannelId, resolving the token only at write time.
//   - updateConnector — the credential is keyed by the connector's id.
//
// What it refuses is a BLANK new token, which is not a rename anyone can have meant:
// it leaves the record addressable by nothing. Whitespace IS trimmed here, because
// unlike the comparison above this is a validity question about one string, and a
// token of "   " is as unusable as "".
func ErrRenameTokenUnusable(entity, token, requested string) error {
	if strings.TrimSpace(requested) != "" {
		return nil
	}
	return fmt.Errorf("cannot update %s %q through a request with a blank token: the payload "+
		"token names what the record should be called, and a blank one would leave it "+
		"addressable by nothing — send the current token to keep it", entity, token)
}
