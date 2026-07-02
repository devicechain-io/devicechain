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
// satisfy (ADR-042 P2). It is a *security* grammar, not a house-style one: its
// only job is to keep a token safe everywhere tokens are spliced into
// infrastructure namespaces. A tenant token becomes the middle segment of a NATS
// subject (messaging.ScopedSubject → "inst.<tenant>.suffix", recovered by
// splitting on "."), so a "." shifts subject segments and "*"/">" inject NATS
// wildcards that match across tenants; "/", "+" and "#" are likewise hazardous on
// MQTT topics; whitespace and other punctuation break subject/URL/log handling.
//
// It therefore allows letters (either case), digits, hyphen and underscore, and
// nothing else — which admits machine-supplied identifiers like uppercase device
// serials and VINs (the platform's own sample data), while still rejecting every
// metacharacter above. Case-folding a token to a lowercase-kebab house style is a
// *presentation* concern owned by the console/masks (ADR-042 P3), not enforced
// here: the backend rejects an unsafe token rather than silently rewriting an
// identifier a device or client chose.
var tokenGrammar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidateToken reports whether a token conforms to the global grammar. It is the
// fail-closed guard applied to every token at create/update by the callbacks
// RegisterTokenGrammar installs; it is also exported so any explicit generation
// path can check a candidate.
func ValidateToken(token string) error {
	if token == "" {
		return fmt.Errorf("token must not be empty")
	}
	if len(token) > MaxTokenLen {
		return fmt.Errorf("token %q exceeds the maximum length of %d", token, MaxTokenLen)
	}
	if !tokenGrammar.MatchString(token) {
		return fmt.Errorf("token %q is invalid: must be letters, digits, hyphens and underscores, starting with a letter or digit (%s)", token, tokenGrammar.String())
	}
	return nil
}

// RegisterTokenGrammar installs global GORM Before callbacks that enforce the
// token grammar for any model whose schema exposes a Token field (embedded
// TokenReference or a direct declaration). Like the tenant-scope callbacks, the
// guard is applied once here, not at each call site, so it is un-skippable by
// construction and covers all token entities across every service uniformly.
//
//   - Create: the token is required and must be valid (a missing/empty token is
//     rejected — the not-null column would allow the empty string, which is not a
//     valid identifier).
//   - Update: the token is validated when it is set. A partial update that does
//     not touch the token (e.g. toggling Enabled) passes through. Note that a
//     struct-destination update cannot distinguish "token field omitted" from
//     "token explicitly set to empty", so an empty token can only be rejected on
//     the create path and the map-update path, not on a whole-struct Save; no
//     call site sets a token that way (updates look the row up by its token
//     first).
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

		// Map destination (Model(&T{}).Updates(map{...}) or Create(map{...})): the
		// value lives in Dest, not ReflectValue.
		if m, ok := db.Statement.Dest.(map[string]interface{}); ok {
			checkMapToken(db, field, m, requireToken)
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

// checkRowToken validates one row's token, handling struct rows and (for batch
// map creates) map rows and interface/pointer wrappers. It returns false — after
// recording the error on db — when the token is invalid, so a batch aborts on the
// first bad row rather than reporting only the last.
func checkRowToken(db *gorm.DB, field *schema.Field, rv reflect.Value, requireToken bool) bool {
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		if m, ok := rv.Interface().(map[string]interface{}); ok {
			return checkMapToken(db, field, m, requireToken)
		}
		return true // a non string-keyed map is not a token payload
	case reflect.Struct:
		val, isZero := field.ValueOf(db.Statement.Context, rv)
		if isZero {
			if requireToken {
				_ = db.AddError(ValidateToken(""))
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
	default:
		return true
	}
}

// checkMapToken validates the token entry of a map destination when present. GORM
// resolves a map key through the schema, accepting both the column name and the Go
// field name, so both are checked. A present-but-unvalidatable value (a
// clause.Expr, a non-string, ...) fails closed — the guard must not be silently
// skippable.
func checkMapToken(db *gorm.DB, field *schema.Field, m map[string]interface{}, requireToken bool) bool {
	v, present := lookupMapToken(m, field)
	if !present {
		if requireToken {
			_ = db.AddError(ValidateToken(""))
			return false
		}
		return true // update not touching the token
	}
	var token string
	switch t := v.(type) {
	case string:
		token = t
	case []byte:
		token = string(t)
	default:
		_ = db.AddError(fmt.Errorf("token must be set as a string, got %T", v))
		return false
	}
	if err := ValidateToken(token); err != nil {
		_ = db.AddError(err)
		return false
	}
	return true
}

// lookupMapToken finds the token entry in a map destination by column name or Go
// field name (GORM accepts either).
func lookupMapToken(m map[string]interface{}, field *schema.Field) (interface{}, bool) {
	if v, ok := m[field.DBName]; ok {
		return v, true
	}
	if v, ok := m[field.Name]; ok {
		return v, true
	}
	return nil, false
}
