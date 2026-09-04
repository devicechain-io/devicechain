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
// ApplyTo and ApplyToValue on the Optional* types cover the two columns a partial
// update usually meets — a nullable one, where an explicit null clears, and a
// non-pointer one, where "clear" is spelled as the zero value. Neither is right for
// a column that is NOT NULL and whose zero value is not a state the entity may be
// in: a metric's data type, a credential's type, a group's member type, a
// provisioning profile's strategy.
//
// ApplyToValue is a FAIL-OPEN for those. `enabled: null` reads as false,
// `dataType: null` reads as "", and both are written successfully — the caller is
// told their update worked, and the row now holds a value the create path would
// have refused. There is no reading of "clear this required field" that leaves a
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
// # Blank strings
//
// The string fold refuses a whitespace-only value as well as an explicit null, and
// TRIMS what it accepts. That mirrors resolveRequiredTypeRef in device-management,
// which is the same decision for the reference case: "   " is a null spelled
// differently, and accepting it stores a value nothing can match. Trimming is stated
// rather than left to each caller because the alternative — some required strings
// trimmed and some not — is how two rows end up holding "acme" and "acme " and being
// the same value to a human and different to every query.

// ApplyToRequired folds a String field onto a NOT NULL column: absent keeps the
// stored value, a value sets it (trimmed), and an explicit null — or a whitespace-only
// string, which is the same request spelled differently — is REFUSED.
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
	trimmed := strings.TrimSpace(*o.Value)
	if trimmed == "" {
		return "", fmt.Errorf("%s cannot be blank: it is required, and a whitespace-only "+
			"value would store something nothing can match", field)
	}
	return trimmed, nil
}

// ApplyToRequired folds a Boolean field onto a NOT NULL column.
//
// 🔴 A boolean is exactly where ApplyToValue is most dangerous and least visible: it
// turns `enabled: null` into `enabled: false`, which disables a credential or a
// provisioning profile and returns success. false is a legal value, so nothing
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
