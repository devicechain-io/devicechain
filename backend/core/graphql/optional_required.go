// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// THE THIRD FOLD: a field the caller may LEAVE ALONE or SET, but never CLEAR.
//
// ApplyTo covers the column a partial update usually meets — a nullable one, where an
// explicit null clears. It is not right for a column that is NOT NULL and whose zero
// value is not a state the entity may be in: a metric's data type, a credential's
// type, a group's member type, a provisioning profile's strategy.
//
// Folding a null to the zero value is a FAIL-OPEN for those. `enabled: null` would
// read as false, `dataType: null` as "", and both would be written successfully — the
// caller told their update worked, and the row now holding a value the create path
// would have refused. There is no reading of "clear this required field" that leaves a
// valid row behind, so the answer is to refuse, and to say which field.
//
// # Why this and not "just validate afterwards"
//
// A vocabulary check downstream would catch `dataType: null` (it lands as "", which
// is not a valid data type). It would NOT catch `enabled: null`, because false is a
// perfectly good boolean, nor a required Int cleared to 0, nor any required field
// whose zero value happens to be legal. Validating after the fold can only see the
// value, and the value has by then lost the distinction that made it wrong.
//
// # 🔴 BLANK STRINGS ARE REFUSED. WHAT IS ACCEPTED IS STORED VERBATIM.
//
// The string fold refuses a whitespace-only value as well as an explicit null: "   "
// is a null spelled differently, and accepting it stores something nothing can match.
//
// It does NOT trim what it accepts, and that reversal is the whole point of this
// paragraph. An earlier version did, on the reasoning that trimming keeps "acme" and
// "acme " from being two values a human reads as one. The reasoning is fine and the
// place is wrong: no create path on this platform trims, so an update that trimmed
// made RESTATING A FIELD change it. That is not theoretical — a provisioning profile
// created with " s3cret " (legal today) and then updated by any client that re-sends
// the fields it read back is left holding "s3cret", the whole fleet stops
// authenticating, and the edit that did it returned 200. Clients that restate exist:
// the simulator's ensure* paths send a full restatement on every convergence pass.
//
// So the rule is: an update may not change a value the caller did not mean to change,
// and "I sent you back exactly what you gave me" must be a no-op. Normalizing input is
// a decision for the create path to make, once, for both paths — not for the fold that
// exists to stop updates from silently rewriting what is stored.
//
// References are the deliberate exception and they are folded elsewhere:
// resolveRequiredTypeRef in device-management DOES trim, because a token has a grammar
// that forbids surrounding whitespace, so trimming there cannot change which record is
// named. Nothing here has a grammar.

// ApplyToRequired folds a String field onto a NOT NULL column: absent keeps the stored
// value, a value sets it VERBATIM, and an explicit null — or a whitespace-only string,
// which is the same request spelled differently — is REFUSED.
//
// field is the name the SCHEMA gives the field, so the refusal names something the
// caller can act on rather than a Go identifier they have never seen.
func (o OptionalString) ApplyToRequired(field string, current string) (string, error) {
	if !o.Set {
		return current, nil
	}
	if o.Value == nil {
		return "", errRequiredFieldCleared(field)
	}
	// TrimSpace is used to DECIDE, never to transform: a value that is nothing but
	// whitespace is refused, and anything else is stored exactly as it arrived.
	if strings.TrimSpace(*o.Value) == "" {
		return "", fmt.Errorf("%s cannot be blank: it is required, and a whitespace-only "+
			"value would store something nothing can match", field)
	}
	return *o.Value, nil
}

// ApplyToRequired folds a Boolean field onto a NOT NULL column.
//
// 🔴 A boolean is where folding a null to the zero value is most dangerous and least
// visible: `enabled: null` would become `enabled: false`, which disables a credential
// or a provisioning profile and returns success. false is a legal value, so nothing
// downstream can tell it from a deliberate one.
func (o OptionalBool) ApplyToRequired(field string, current bool) (bool, error) {
	if !o.Set {
		return current, nil
	}
	if o.Value == nil {
		return false, errRequiredFieldCleared(field)
	}
	return *o.Value, nil
}

// ApplyToRequired folds an Int field onto a NOT NULL column.
func (o OptionalInt32) ApplyToRequired(field string, current int32) (int32, error) {
	if !o.Set {
		return current, nil
	}
	if o.Value == nil {
		return 0, errRequiredFieldCleared(field)
	}
	return *o.Value, nil
}

// ApplyToRequired folds a Float field onto a NOT NULL column.
func (o OptionalFloat64) ApplyToRequired(field string, current float64) (float64, error) {
	if !o.Set {
		return current, nil
	}
	if o.Value == nil {
		return 0, errRequiredFieldCleared(field)
	}
	return *o.Value, nil
}

func errRequiredFieldCleared(field string) error {
	return fmt.Errorf("%s cannot be cleared: it is required, so send a value to change it "+
		"or omit it to leave it alone", field)
}

// ApplyToNullTime folds a String field onto a NULLABLE timestamp column.
//
// The three states are the usual ones — absent keeps, null clears, a value sets —
// but the value has to be PARSED, and that is why this lives here rather than being
// spelled out at each call site. Two things go wrong when it is:
//
//   - the parse failure and the clear get conflated. The shape this replaces read
//     `if request.ExpiresAt != nil { parse }`, so a caller who sent nothing and a
//     caller who sent a malformed timestamp were told apart only by an error the
//     first could not produce; under three states the same shape would make an
//     ABSENT field clear the column.
//   - the layout drifts. RFC3339 is what the create paths accept, so an update that
//     accepted anything else would let a value in through a door its own create
//     mutation keeps shut.
//
// An empty string is treated as a CLEAR rather than a parse error. It is what a form
// sends for "no expiry", and refusing it would leave the API with no way to remove an
// expiry that a caller could discover.
func (o OptionalString) ApplyToNullTime(field string, current sql.NullTime) (sql.NullTime, error) {
	if !o.Set {
		return current, nil
	}
	if o.Value == nil || strings.TrimSpace(*o.Value) == "" {
		return sql.NullTime{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*o.Value))
	if err != nil {
		return sql.NullTime{}, fmt.Errorf("%s must be an RFC3339 timestamp: %w", field, err)
	}
	return sql.NullTime{Time: parsed, Valid: true}, nil
}
