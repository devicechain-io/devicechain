// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sqlnull holds the platform's rules for turning an optional input value into a
// nullable column value. It is a leaf — database/sql and strings, nothing else — so that
// both rdb (every create path) and graphql (every partial-update fold) can use ONE
// definition of each rule without graphql pulling rdb's database drivers into every
// module that only serves a schema.
//
// The rules are different on purpose, and which one a column gets is a decision about
// what the column HOLDS:
//
//	Text    descriptive text (a name, a description, an icon, a unit): trimmed, and a
//	        value that is empty after trimming is stored as NULL
//	Secret  secret material compared byte for byte (a device password): stored exactly
//	        as sent; only nil or "" store NULL
//	Int64FromInt32  a 32-bit GraphQL Int into a bigint column: widens, never narrows
package sqlnull

import (
	"database/sql"
	"strings"
)

// Text is the platform's rule for NULLABLE DESCRIPTIVE TEXT: surrounding whitespace is
// trimmed, and a value that is empty after trimming is stored as NULL. Create paths (via
// rdb.NullStrOf) and the partial-update fold (graphql.OptionalString.ApplyToNullString)
// both apply it, so restating a value read back from a row written through either is a
// no-op.
//
// 🔴 NOT FOR SECRETS. A credential is compared byte for byte against what a device
// presents, so trimming it on save makes a password with surrounding whitespace one
// nothing can present. Use Secret.
func Text(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: trimmed, Valid: true}
}

// Secret stores secret material EXACTLY AS SENT, surrounding whitespace included. nil and
// the empty string store NULL — "no secret" — and every other value, a whitespace-only one
// too, is stored verbatim: it is a value a device can present, so it is a value the
// compare must be able to match.
func Secret(value *string) sql.NullString {
	if value == nil || *value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

// Int64FromInt32 stores an optional GraphQL Int — 32-bit by specification — in a nullable
// bigint column. It only widens, so it cannot lose a value. nil stores NULL.
func Int64FromInt32(value *int32) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}
