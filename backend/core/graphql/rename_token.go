// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"strings"
)

// MAY A RENAME LEAVE A RECORD NAMED BY NOTHING? No, and that is the whole of what
// this file decides.
//
// # The question this file USED to answer, and why it is gone
//
// It was "which record does an update write?", and it existed because updates
// reused their create input. Such a request carried the token TWICE — once in the
// `token:` argument to say which record, once inside the shared payload — and the
// two could disagree. Most areas located the row by the PAYLOAD token and ignored
// the argument, so a request naming one record and payloading another silently
// updated the second and returned it with a success; the rest honoured the argument
// and then wrote the payload token over the stored one, so an empty payload token —
// which `token: String!` admits, "" being a perfectly good non-null String — BLANKED
// the record's token and returned success over a row addressable by nothing.
//
// Both halves are unreachable now. Every update* mutation on the platform takes a
// dedicated *UpdateRequest, and none of those inputs carries a token at all, so
// naming a second record is unrepresentable rather than refused. The reconcile rule
// that used to say so (ErrPayloadTokenDisagrees) was deleted with its last caller:
// a function guarding a field that no longer exists is not a safety net, it is a
// claim about a shape the schema stopped having. `putest.AssertEveryUpdateTakesADedicatedRequest`
// is what keeps it that way — it asks each request TYPE whether it has a Token
// field, so a token reintroduced into any update input fails on the day it is added,
// which a runtime reconcile check could only ever discover one request at a time.
//
// # What survived is the RENAME, which was a real capability rather than an accident
//
// Renaming was the one legitimate reason an update payload ever carried a token, so
// it moved to purpose-built `rename*` mutations taking `(token, newToken)`, where
// newToken can mean only one thing. The blank check below is that class's floor. It
// has nothing to do with updates any more, and it is not weaker for that: whether
// the blank arrives through a shared create input or through a dedicated argument,
// a record whose token is "" is a record nothing can ever name again.

// ErrRenameTokenUnusable refuses a rename onto a BLANK token.
//
// A blank is not a rename anyone can have meant: it leaves the record addressable by
// nothing, which is exactly what used to happen, successfully. Whitespace IS trimmed,
// unlike the token grammar's own checks, because this is a validity question about
// one string and a token of "   " is as unusable as "". The value is never trimmed
// for STORAGE by any caller — trimming there would silently accept " tok " as naming
// "tok" while nothing in the system stores the trimmed form.
//
// # 🔴 WHAT IT DOES NOT CHECK, AND WHY EACH RENAME NEEDS MORE THAN THIS
//
// This is the floor, not the contract. A rename also has to be idempotent on its own
// token, has to refuse a token another row holds — under contention as well as
// without it, which is what rdb.IsUniqueViolation is for — and may carry a rule of
// its own: updateDeviceProfile's rename is refused outright once the profile is
// published or adopted, because published rules and dead-man rosters key on the
// token from that point on. None of that can live here; all of it lives with the
// mutation.
//
// # 🔴 THE CALLERS ARE NOT LISTED, AND THAT IS DELIBERATE
//
// An earlier version of this comment enumerated them and said "only three" while
// there were four, which is what a count frozen in prose does. `grep -rn
// ErrRenameTokenUnusable` is the authority and it costs one command.
//
// The same grep will show that most of this platform's renames do NOT call this,
// and that is not drift. Measured across all four: every one applies the identical
// predicate, `strings.TrimSpace(newToken) == ""`, and the three that inline it do so
// to say what the blank costs for THAT entity — a profile "can never be found
// again", a connector "can never be dispatched to again", a provider "can never be
// granted or assigned again". That clause is the useful half of the sentence and it
// is not derivable from an entity noun, which is the honest reason this function has
// one caller rather than four. What is shared is the rule; what differs is only the
// consequence, and a rename that got the rule wrong would be caught by its own
// service's blank-token test rather than by this function's absence.
func ErrRenameTokenUnusable(entity, token, newToken string) error {
	if strings.TrimSpace(newToken) != "" {
		return nil
	}
	return fmt.Errorf("cannot rename %s %q to a blank token: the new token is what the "+
		"record will be called, and a blank one would leave it addressable by nothing — "+
		"send the current token to leave the name alone", entity, token)
}
