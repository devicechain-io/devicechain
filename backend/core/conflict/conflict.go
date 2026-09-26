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
// # Why only 23505
//
// Foreign-key (23503) and check (23514) violations are a separate decision: they are not
// "a value that must be unique is already in use", and answering them with this code
// would tell a client that retries-as-exists to carry on. They pass through unchanged.
//
// # Why SQLite is recognised by package path rather than by importing its type
//
// No production binary links SQLite; only tests do. Importing the driver's error type
// into a package every service links would put a transpiled C database into every
// service binary to recognise an error production can never produce. The SQLite branch
// therefore matches the error's TYPE IDENTITY (its package path) and its numeric Code()
// — still a type check, never a text match. The path is pinned by a test over a REAL
// SQLite error (in core/rdb, whose tests already use the driver), so a driver rename
// fails a test instead of silently classifying nothing. That test is not in this package
// on purpose: dcctl links this package, and a driver imported by its tests would enter
// dcctl's module graph.
//
// # Why this is a leaf package
//
// dcctl and core/graphql need the code without linking gorm, so it lives outside rdb and
// imports only the standard library and pgconn.
//
// # 🔴 WHAT IS NOT A CONFLICT, and must never be made one
//
// A client that treats Code as "the record already exists, carry on" must never see it
// on a refusal where carrying on is wrong. Two such refusals exist and each carries a
// test pinning that it is not a conflict:
//
//   - a deleted tenant's reserved token (user-management's ErrTenantTokenReserved): the
//     token is held by a tenant nobody can enter;
//   - a stale-version save (the ErrConflict sentinels in ai-inference,
//     dashboard-management and outbound-connectors, "modified by another writer; reload
//     and try again"): despite the name, that is a lost update, not a taken value.
package conflict

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
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

// sqlitePkgPath is the package the SQLite driver's *Error type is declared in (see the
// package doc for why it is matched by path rather than imported).
const sqlitePkgPath = "github.com/glebarez/go-sqlite"

// SQLite's extended result codes for a uniqueness failure. The driver enables extended
// codes, so these are what Code() reports.
const (
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
)

// postgresUniqueViolation is the SQLSTATE of a unique violation.
const postgresUniqueViolation = "23505"

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
func As(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	if drv, constraint, ok := driverViolation(err); ok {
		return &Error{Constraint: constraint, cause: drv}, true
	}
	return nil, false
}

// Is reports whether err's chain holds a conflict (see As).
func Is(err error) bool {
	_, ok := As(err)
	return ok
}

// Redact removes the text of any driver unique violation in err's chain from message,
// which is what a GraphQL response would otherwise serve. It looks for the violation
// independently of any *Error above it, so a service refusal joined with a driver error
// cannot shield the driver's text.
//
//   - The driver error's full text, where it appears, is replaced by Message, keeping any
//     context the service added around it.
//   - If a fragment that leaks still remains (the Postgres message or constraint name, or
//     the SQLite message without its code suffix — for example because a caller printed
//     part of the error with %v), the WHOLE message is replaced by Message.
//   - A message holding no fragment is returned unchanged: a service's own sentence is
//     kept.
//
// constraint is the violated constraint's name, for server-side logging; changed
// reports whether message was altered.
func Redact(message string, err error) (redacted, constraint string, changed bool) {
	drv, constraint, ok := driverViolation(err)
	if !ok {
		return message, "", false
	}
	out := message
	if full := drv.Error(); full != "" {
		out = strings.ReplaceAll(out, full, Message)
	}
	for _, frag := range leakFragments(drv) {
		if frag != "" && strings.Contains(out, frag) {
			return Message, constraint, true
		}
	}
	return out, constraint, out != message
}

// leakFragments are the parts of a driver violation that identify the database's
// internals, and so must not survive a redaction on their own.
func leakFragments(drv error) []string {
	var pg *pgconn.PgError
	if errors.As(drv, &pg) {
		return []string{pg.Message, pg.ConstraintName, pg.Detail}
	}
	// SQLite prints "<errstr>: <errmsg> (<code>)"; the part before the code suffix is
	// what a partial print would carry.
	msg := drv.Error()
	if i := strings.LastIndex(msg, " ("); i > 0 {
		msg = msg[:i]
	}
	return []string{msg}
}

// driverViolation walks err's whole chain — both Unwrap forms — for the first driver
// unique violation. It does not use errors.As with an interface target, which would stop
// at the FIRST error with a Code() method whatever its package.
func driverViolation(err error) (drv error, constraint string, ok bool) {
	walk(err, func(e error) bool {
		if pg, isPg := e.(*pgconn.PgError); isPg {
			if pg.Code == postgresUniqueViolation {
				drv, constraint, ok = pg, pg.ConstraintName, true
				return true
			}
			return false
		}
		if isSQLiteUnique(e) {
			drv, ok = e, true
			return true
		}
		return false
	})
	return drv, constraint, ok
}

// isSQLiteUnique matches the SQLite driver's error by type identity and numeric code.
func isSQLiteUnique(e error) bool {
	t := reflect.TypeOf(e)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().PkgPath() != sqlitePkgPath {
		return false
	}
	coded, ok := e.(interface{ Code() int })
	if !ok {
		return false
	}
	switch coded.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	}
	return false
}

// walk visits err and every error beneath it, depth first, until visit returns true.
func walk(err error, visit func(error) bool) bool {
	if err == nil {
		return false
	}
	if visit(err) {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return walk(u.Unwrap(), visit)
	case interface{ Unwrap() []error }:
		for _, inner := range u.Unwrap() {
			if walk(inner, visit) {
				return true
			}
		}
	}
	return false
}
