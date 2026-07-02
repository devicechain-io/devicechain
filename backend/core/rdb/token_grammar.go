// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"
	"regexp"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// tokenFieldName is the Go struct field name carried by TokenReference (and
// declared directly by the few token entities that do not embed it, e.g. the
// user-management Tenant and Role). A schema exposing this field is token-keyed.
const tokenFieldName = "Token"

// MaxTokenLen bounds a token's length, matching the size:128 storage column on
// TokenReference.Token.
const MaxTokenLen = 128

// tokenGrammar is the single, type-independent grammar every entity token must
// satisfy (ADR-042 P2): lowercase letters and digits and hyphens, beginning with
// a letter or digit. It deliberately excludes the metacharacters that would make
// a token unsafe where tokens are spliced into infrastructure namespaces —
// notably a tenant token becomes the middle segment of a NATS subject
// (messaging.ScopedSubject → "inst.<tenant>.suffix", recovered by splitting on
// "."), so a "." shifts subject segments and "*"/">" inject NATS wildcards that
// match across tenants. "/", "+" and "#" are likewise hazardous on MQTT topics.
var tokenGrammar = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateToken reports whether a token conforms to the global grammar. It is the
// fail-closed guard applied to every token at create/update by the callbacks
// RegisterTokenGrammar installs; it is also exported so any explicit generation
// path can check a candidate. Normalization of human input (case-folding,
// kebab-casing) is a client/console concern (ADR-042 P3), not done here — the
// backend rejects a non-conforming token rather than silently rewriting an
// identifier.
func ValidateToken(token string) error {
	if token == "" {
		return fmt.Errorf("token must not be empty")
	}
	if len(token) > MaxTokenLen {
		return fmt.Errorf("token %q exceeds the maximum length of %d", token, MaxTokenLen)
	}
	if !tokenGrammar.MatchString(token) {
		return fmt.Errorf("token %q is invalid: must be lowercase letters, digits and hyphens, starting with a letter or digit (%s)", token, tokenGrammar.String())
	}
	return nil
}

// RegisterTokenGrammar installs global GORM Before callbacks that enforce the
// token grammar for any model whose schema exposes a Token field (Q: embedded
// TokenReference or a direct declaration). Like the tenant-scope callbacks, the
// guard is applied once here, not at each call site, so it is un-skippable by
// construction and covers all ~21 token entities across every service uniformly.
//
//   - Create: the token is required and must be valid (a missing/empty token is
//     rejected — the not-null column would allow the empty string, which is not a
//     valid identifier).
//   - Update: the token is validated only when it is actually being set, so a
//     partial update that does not touch the token (e.g. toggling Enabled) passes
//     through. Tokens are stable identifiers and are rarely updated, but a change
//     to an unsafe value must still be rejected.
//
// Models without a Token field pass through untouched.
func RegisterTokenGrammar(db *gorm.DB) error {
	for _, register := range []func() error{
		func() error {
			return db.Callback().Create().Before("gorm:create").Register("dc:token_grammar_create", tokenGrammarCheck(true))
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").Register("dc:token_grammar_update", tokenGrammarCheck(false))
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// tokenGrammarCheck builds the Before-callback. requireToken distinguishes create
// (the token must be present) from update (validate only when the token is set).
func tokenGrammarCheck(requireToken bool) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if db.Error != nil || !ensureSchema(db) {
			return
		}
		field, ok := db.Statement.Schema.FieldsByName[tokenFieldName]
		if !ok {
			return // not a token entity
		}

		// Map-based updates (Model(&T{}).Updates(map{"token": ...})) carry the new
		// value in Dest, not ReflectValue; validate it there when present.
		if m, ok := db.Statement.Dest.(map[string]interface{}); ok {
			if v, present := m[field.DBName]; present {
				if s, ok := v.(string); ok {
					if err := ValidateToken(s); err != nil {
						_ = db.AddError(err)
					}
				}
			}
			return
		}

		// Struct / slice / array destinations: validate each row's token.
		rv := db.Statement.ReflectValue
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				if !checkRowToken(db, field, rv.Index(i), requireToken) {
					return
				}
			}
		case reflect.Struct:
			checkRowToken(db, field, rv, requireToken)
		}
	}
}

// checkRowToken validates one row's token. It returns false (and records the
// error on db) when the token is invalid, so a batch insert aborts on the first
// bad row rather than reporting only the last.
func checkRowToken(db *gorm.DB, field *schema.Field, rv reflect.Value, requireToken bool) bool {
	val, isZero := field.ValueOf(db.Statement.Context, rv)
	if isZero {
		if requireToken {
			_ = db.AddError(fmt.Errorf("token must not be empty"))
			return false
		}
		return true // update not touching the token
	}
	token, _ := val.(string)
	if err := ValidateToken(token); err != nil {
		_ = db.AddError(err)
		return false
	}
	return true
}
