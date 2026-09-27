// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package integrity recognises a database INTEGRITY refusal from the driver's error TYPE
// and code — never its text — and gives the two non-uniqueness classes their wire code
// and neutral sentence. Uniqueness keeps its own code and sentence in core/conflict,
// which uses this package to recognise it.
//
// # The three classes, and why two codes rather than one
//
//   - Unique (Postgres 23505; SQLite 2067, 1555): a value that must be unique is taken.
//     Answered by core/conflict with CONFLICT.
//   - Reference (Postgres 23503, 23001; SQLite 787): the write depends on how records
//     refer to each other — it names a record that is gone, or removes one that others
//     still refer to. Answered with REFERENCE_VIOLATION. The client's next move is about
//     OTHER records: reload, remove or restore what refers, and a lost race may succeed
//     on retry.
//   - Invalid (every other SQLSTATE of class 23 — check 23514, not-null 23502, exclusion
//     23P01, the bare 23000 — and every other SQLite constraint code): a value the record
//     does not allow reached the database. Answered with INVALID_VALUE. The client's next
//     move is about THE REQUEST ITSELF.
//
// One code for both would send a client back to reading prose to tell them apart. The
// classification is FAIL-CLOSED: a class-23 state not named above is Invalid, never
// passed through with the database's wording, and never Unique — CONFLICT is the one
// code a client treats as "already exists, carry on" (dcctl does), so nothing that is
// not a taken value may carry it. Exclusion (23P01) is Invalid for that reason even
// though it depends on other rows. Errors outside class 23 (a serialization failure, a
// truncation) are not integrity refusals and are not recognised here.
//
// # 🔴 WHY A TYPE AND NOT PROSE
//
// A message that CONTAINS "23503" is not a violation here; only a *pgconn.PgError whose
// Code is in class 23, or a SQLite driver error whose numeric code is a constraint
// code, is. gorm's error translation (TranslateError) is not enabled anywhere; it would
// replace the driver error with a gorm sentinel and this package would recognise
// nothing.
//
// # Why SQLite is recognised by package path rather than by importing its type
//
// No production binary links SQLite; only tests do. Importing the driver's error type
// into a package every service links would put a transpiled C database into every
// service binary to recognise an error production can never produce. The SQLite branch
// therefore matches the error's TYPE IDENTITY (its package path) and its numeric Code()
// — still a type check, never a text match. The path is pinned by tests over REAL
// SQLite errors in core/rdb, whose tests already use the driver, so a driver rename
// fails a test instead of silently classifying nothing. Those tests are not in this
// package on purpose: dcctl links this package (through core/conflict), and a driver
// imported by its tests would enter dcctl's module graph.
//
// # Why this is a leaf package
//
// dcctl and core/graphql need it without linking gorm, so it imports only the standard
// library and pgconn.
package integrity

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Class is the kind of integrity refusal.
type Class uint8

const (
	// ClassUnique is a taken unique value; core/conflict answers it with CONFLICT.
	ClassUnique Class = iota + 1
	// ClassReference is a refused reference between records (REFERENCE_VIOLATION).
	ClassReference
	// ClassInvalid is any other refused value (INVALID_VALUE).
	ClassInvalid
)

// String names the class for server-side logs. It does not panic on an unknown value:
// it runs in the log path of the WebSocket pump, where a panic would take the process
// down rather than fail one request.
func (c Class) String() string {
	switch c {
	case ClassUnique:
		return "unique"
	case ClassReference:
		return "reference"
	case ClassInvalid:
		return "invalid"
	}
	return "unknown"
}

// The wire codes and neutral sentences of the two non-uniqueness classes. A sentence
// names no table, column or constraint and repeats no value. Neither contains a word a
// dcctl from before CONFLICT existed took for a duplicate ("already exists",
// "duplicate", "unique"), so an older client does not carry on over either.
const (
	CodeReference    = "REFERENCE_VIOLATION"
	CodeInvalid      = "INVALID_VALUE"
	MessageReference = "the request refers to a record that does not exist, or removes one that other records still refer to"
	MessageInvalid   = "the request contains a value this record does not allow"
)

// Answer is the wire code and neutral sentence of a non-unique class. ok is false for
// ClassUnique (core/conflict owns its answer) and for any unknown class: the caller must
// not invent one.
func Answer(c Class) (code, message string, ok bool) {
	switch c {
	case ClassReference:
		return CodeReference, MessageReference, true
	case ClassInvalid:
		return CodeInvalid, MessageInvalid, true
	}
	return "", "", false
}

// Violation is a driver integrity error found in an error chain.
type Violation struct {
	Class Class
	// Driver is the *pgconn.PgError or SQLite error itself.
	Driver error
	// The rest are for server-side diagnostics only and are never served. Constraint,
	// Table and Column are what Postgres reported ("" for SQLite, and Constraint is ""
	// for a not-null violation); SQLState is the Postgres code, or the SQLite extended
	// code in decimal.
	Constraint string
	Table      string
	Column     string
	SQLState   string
}

// sqlitePkgPath is the package the SQLite driver's *Error type is declared in (see the
// package doc for why it is matched by path rather than imported).
const sqlitePkgPath = "github.com/glebarez/go-sqlite"

// SQLite's extended result codes. The driver enables extended codes, so these are what
// Code() reports. Every constraint code has SQLITE_CONSTRAINT (19) as its low byte.
const (
	sqliteConstraint           = 19   // SQLITE_CONSTRAINT
	sqliteConstraintForeignKey = 787  // SQLITE_CONSTRAINT_FOREIGNKEY
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
)

// pgClass classifies a Postgres SQLSTATE. Only class 23 is an integrity violation.
func pgClass(code string) (Class, bool) {
	if len(code) != 5 || code[:2] != "23" {
		return 0, false
	}
	switch code {
	case "23505": // unique_violation
		return ClassUnique, true
	case "23503", "23001": // foreign_key_violation, restrict_violation
		return ClassReference, true
	}
	return ClassInvalid, true // fail closed: every other integrity state
}

// sqliteClass matches the SQLite driver's error by type identity and numeric code.
func sqliteClass(e error) (int, Class, bool) {
	t := reflect.TypeOf(e)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().PkgPath() != sqlitePkgPath {
		return 0, 0, false
	}
	coded, ok := e.(interface{ Code() int })
	if !ok {
		return 0, 0, false
	}
	code := coded.Code()
	if code&0xff != sqliteConstraint {
		return 0, 0, false
	}
	switch code {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return code, ClassUnique, true
	case sqliteConstraintForeignKey:
		return code, ClassReference, true
	}
	return code, ClassInvalid, true // fail closed: every other constraint code
}

// violation reports whether e ITSELF (not its chain) is a driver integrity violation.
func violation(e error) (Violation, bool) {
	if pg, isPg := e.(*pgconn.PgError); isPg {
		c, ok := pgClass(pg.Code)
		if !ok {
			return Violation{}, false
		}
		return Violation{Class: c, Driver: pg, Constraint: pg.ConstraintName,
			Table: pg.TableName, Column: pg.ColumnName, SQLState: pg.Code}, true
	}
	if code, c, ok := sqliteClass(e); ok {
		return Violation{Class: c, Driver: e, SQLState: strconv.Itoa(code)}, true
	}
	return Violation{}, false
}

// All returns every driver violation in err's chain — both Unwrap forms, depth first —
// in walk order. It does not use errors.As with an interface target, which would stop
// at the FIRST error with a Code() method whatever its package.
func All(err error) []Violation {
	var out []Violation
	walk(err, func(e error) bool {
		if v, ok := violation(e); ok {
			out = append(out, v)
		}
		return false
	})
	return out
}

// Refused returns the class of the first NON-UNIQUE integrity refusal in err's chain, in
// walk order: a *Refusal, or a driver reference or invalid violation. A unique violation
// is skipped, not an answer.
//
// 🔴 This is what decides that a refusal is NOT a conflict, and it is asked BEFORE
// core/conflict is: a chain holding both a taken value and a refused reference is not
// "already exists, carry on", because re-running it cannot succeed. CONFLICT is served
// only when nothing here answers.
func Refused(err error) (Class, bool) {
	var found Class
	walk(err, func(e error) bool {
		// A zero Refusal (not made by NewRefusal) has no class: it is skipped, so it
		// cannot stop the walk before a refusal that does have one.
		if r, ok := e.(*Refusal); ok && r != nil && r.class != 0 {
			found = r.class
			return true
		}
		if v, ok := violation(e); ok && v.Class != ClassUnique {
			found = v.Class
			return true
		}
		return false
	})
	return found, found != 0
}

// Redact removes v's driver text from message, which is what a response would otherwise
// serve.
//
//   - v's full text, where it appears, is replaced by neutral, keeping any context the
//     service added around it;
//   - whole reports that a fragment identifying the database's internals remains (the
//     Postgres message, constraint name or detail — the detail repeats the values sent —
//     or the SQLite message without its code suffix; for example because a caller
//     printed part of the error with %v). The caller must then replace the WHOLE
//     message;
//   - a message holding no fragment is returned unchanged: a service's own sentence is
//     kept.
func Redact(message string, v Violation, neutral string) (out string, whole bool) {
	out = message
	if full := v.Driver.Error(); full != "" {
		out = strings.ReplaceAll(out, full, neutral)
	}
	for _, frag := range leakFragments(v.Driver) {
		if frag != "" && strings.Contains(out, frag) {
			return out, true
		}
	}
	return out, false
}

// leakFragments are the parts of a driver violation that identify the database's
// internals or repeat the values sent, and so must not survive a redaction on their own.
func leakFragments(drv error) []string {
	if pg, ok := drv.(*pgconn.PgError); ok {
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

// Refusal is a service-authored refusal of a non-unique class: the service's own
// sentence plus the class's wire code. It is meant for sentinel errors, so a client
// sees ONE code whether the service's own check refused a write or the database did
// after a concurrent change; errors.Is keeps working by pointer identity.
type Refusal struct {
	class Class
	msg   string
}

// NewRefusal is a service-authored refusal of class c. It panics for ClassUnique (use
// core/conflict's New) and for any class with no code: a refusal with no code must fail
// when the sentinel is declared, not ship uncoded.
func NewRefusal(c Class, msg string) *Refusal {
	if _, _, ok := Answer(c); !ok {
		panic(fmt.Sprintf("integrity: NewRefusal needs a non-unique class with a code, got %s", c))
	}
	return &Refusal{class: c, msg: msg}
}

// Error returns the service's sentence.
func (r *Refusal) Error() string { return r.msg }

// Class is the refusal's class.
func (r *Refusal) Class() Class { return r.class }

// Extensions gives the refusal its wire code. graphql-go reads it only from the error a
// resolver returned DIRECTLY, which is why the GraphQL boundary also sets it for a
// refusal found deeper in the chain. A fresh map per call: a caller may write into it.
func (r *Refusal) Extensions() map[string]any {
	code, _, _ := Answer(r.class)
	return map[string]any{"code": code}
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
