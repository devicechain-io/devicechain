// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package conflict is the one definition of a UNIQUENESS CONFLICT on this platform: a
// write that collided with a value that must be unique, or a refusal a service makes
// because such a value is already taken.
//
// It carries three things, and nothing else holds a second copy of any of them:
//
//   - Code, the extensions.code a conflict carries on the wire, which is what a client
//     branches on (dcctl does, to make a re-create idempotent);
//   - Message, the neutral sentence served in place of a database's own wording;
//   - As, the recognizer, which reads the DRIVER ERROR TYPE, never its text.
//
// # 🔴 WHY A TYPE AND NOT PROSE
//
// Before this package, the database's own error reached the caller verbatim
// ("duplicate key value violates unique constraint "uix_device_types_tenant_token"
// (SQLSTATE 23505)"), naming internal indexes and giving nothing to branch on, and the
// one client that needed to recognise a duplicate did it by matching phrases — which
// missed a service's own refusal whose wording used none of them. A message that
// CONTAINS "23505" is not a conflict here; only a *pgconn.PgError whose Code is 23505,
// or a SQLite driver error whose numeric code is a unique or primary-key violation, is.
//
// # Why only a taken value
//
// Foreign-key, check, not-null and every other integrity violation are recognised by
// core/integrity, which this package uses to recognise a unique violation too, and are
// answered with their own codes (REFERENCE_VIOLATION, INVALID_VALUE) — never with this
// one. They are not "a value that must be unique is already in use", and answering them
// with CONFLICT would tell a client that retries-as-exists to carry on.
//
// Recognition — the driver's error TYPE and code, never its text, including why SQLite
// is matched by package path and why the tests over a real SQLite error live in
// core/rdb rather than here — lives in core/integrity. This package stays a leaf in the
// same sense: dcctl and core/graphql need the code without linking gorm.
//
// # 🔴 WHAT IS NOT A CONFLICT, and must never be made one
//
// A client that treats Code as "the record already exists, carry on" must never see it
// on a refusal where carrying on is wrong. Two such refusals exist and each carries a
// test pinning that it is not a conflict:
//
//   - a deleted tenant's reserved token (user-management's ErrTenantTokenReserved): the
//     token is held by a tenant nobody can enter;
//   - a stale-version save (rdb.StaleWriteError, which each service offering an
//     `expectedUpdatedAt` precondition declares as its ErrConflict, "modified by another
//     writer; reload and try again"): despite the name, that is a lost update, not a
//     taken value.
package conflict

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/integrity"
)

// Code is the extensions.code a conflict carries on the wire.
const Code = "CONFLICT"

// Message is the neutral sentence served in place of a driver's unique-violation text.
// It names no index, no column and no value.
//
// It contains the word "unique" on purpose: a dcctl from before this code existed
// recognised a duplicate by that word, so an older dcctl still treats a re-create
// against a newer server as idempotent. Keep it in the sentence.
const Message = "the request conflicts with an existing record: a value that must be unique is already in use"

// Error is a conflict: a write collided with, or a service refused because of, a value
// that must be unique. Its Error() never carries driver text.
type Error struct {
	msg string // the service's own sentence; "" means Message
	// Constraint is the violated constraint's name when Postgres reported one. It is
	// for server-side diagnostics only and is never served.
	Constraint string
	cause      error // the driver error when recognised from one; nil for a service refusal
}

// New is a service-authored refusal carrying Code with the service's own sentence.
func New(msg string) *Error { return &Error{msg: msg} }

// Errorf is New with a formatted sentence. %w is NOT supported: the sentence is the
// whole message, and a wrapped cause would be served inside it.
func Errorf(format string, args ...any) *Error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// Error returns the service's sentence, or Message when the conflict was recognised
// from a driver error.
func (e *Error) Error() string {
	if e.msg == "" {
		return Message
	}
	return e.msg
}

// Unwrap returns the driver error this conflict was recognised from, or nil.
func (e *Error) Unwrap() error { return e.cause }

// Extensions gives the error its wire code. graphql-go reads it only from the error a
// resolver returned DIRECTLY, which is why the GraphQL boundary also sets it for a
// conflict found deeper in the chain.
func (e *Error) Extensions() map[string]any { return map[string]any{"code": Code} }

// As reports whether err's chain holds a conflict: a *Error, or a driver unique
// violation (Postgres 23505; SQLite 2067 or 1555). A driver violation is returned
// wrapped in a new *Error whose cause is the driver error. A *Error in the chain wins
// over a driver violation, because it is the service's own account of the refusal.
//
// A conflict in the chain is not by itself the wire answer: the GraphQL boundary serves
// CONFLICT only when core/integrity finds no other kind of refusal beside it (see
// integrity.Refused), because CONFLICT is the one code a client carries on over.
func As(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	for _, v := range integrity.All(err) {
		if v.Class == integrity.ClassUnique {
			return &Error{Constraint: v.Constraint, cause: v.Driver}, true
		}
	}
	return nil, false
}

// Is reports whether err's chain holds a conflict (see As).
func Is(err error) bool {
	_, ok := As(err)
	return ok
}
