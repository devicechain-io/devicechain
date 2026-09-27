// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integrity_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests over REAL SQLite errors live in core/rdb, so that this package's tests do not
// pull a database driver into the module graph of every consumer — dcctl included.

func pg(code string) *pgconn.PgError {
	return &pgconn.PgError{Severity: "ERROR", Code: code, Message: "refused " + code, ConstraintName: "c_" + code}
}

// Postgres classification is by SQLSTATE and fails closed within class 23: a state not
// named explicitly is Invalid, never passed through and never Unique.
func TestPostgresClassification(t *testing.T) {
	for code, want := range map[string]integrity.Class{
		"23505": integrity.ClassUnique,
		"23503": integrity.ClassReference,
		"23001": integrity.ClassReference,
		"23514": integrity.ClassInvalid,
		"23502": integrity.ClassInvalid,
		"23000": integrity.ClassInvalid,
		"23P01": integrity.ClassInvalid,
	} {
		t.Run(code, func(t *testing.T) {
			vs := integrity.All(fmt.Errorf("w: %w", pg(code)))
			require.Len(t, vs, 1)
			assert.Equal(t, want, vs[0].Class)
			assert.Equal(t, code, vs[0].SQLState)
			assert.Equal(t, "c_"+code, vs[0].Constraint)
		})
	}
	for _, code := range []string{"22001", "40001", "08006", "2350", "235050", ""} {
		t.Run("not "+code, func(t *testing.T) {
			assert.Empty(t, integrity.All(fmt.Errorf("w: %w", pg(code))))
			_, ok := integrity.Refused(pg(code))
			assert.False(t, ok)
		})
	}
}

// coded mimics the SQLite driver's error shape from the WRONG package: a constraint
// Code() alone must not make an error a violation.
type coded struct{ code int }

func (c coded) Error() string { return fmt.Sprintf("FOREIGN KEY constraint failed (%d)", c.code) }
func (c coded) Code() int     { return c.code }

// All walks the whole chain, both Unwrap forms, and neither stops at a violation of
// another class nor classifies an unrelated error with a Code() method.
func TestAllAndRefusedWalkTheWholeChain(t *testing.T) {
	fk, unique := pg("23503"), pg("23505")
	err := errors.Join(fmt.Errorf("a: %w", unique), coded{787}, io.EOF, fmt.Errorf("b: %w", fk))

	vs := integrity.All(err)
	require.Len(t, vs, 2)
	assert.Same(t, unique, vs[0].Driver)
	assert.Equal(t, integrity.ClassUnique, vs[0].Class)
	assert.Same(t, fk, vs[1].Driver)
	assert.Equal(t, integrity.ClassReference, vs[1].Class)

	class, ok := integrity.Refused(err)
	require.True(t, ok, "a unique violation earlier in the chain must not end the search")
	assert.Equal(t, integrity.ClassReference, class)

	_, ok = integrity.Refused(fmt.Errorf("w: %w", unique))
	assert.False(t, ok, "a unique violation alone is not a non-unique refusal")
	assert.Empty(t, integrity.All(fmt.Errorf("w: %w", coded{787})))
}

// Refused answers the FIRST non-unique refusal in walk order, service refusal or driver
// violation alike.
func TestRefusedAnswersTheFirstNonUniqueRefusal(t *testing.T) {
	inUse := integrity.NewRefusal(integrity.ClassReference, "in use")
	class, ok := integrity.Refused(errors.Join(pg("23514"), fmt.Errorf("x: %w", inUse)))
	require.True(t, ok)
	assert.Equal(t, integrity.ClassInvalid, class)

	class, ok = integrity.Refused(errors.Join(fmt.Errorf("x: %w", inUse), pg("23514")))
	require.True(t, ok)
	assert.Equal(t, integrity.ClassReference, class)
}

func TestRedact(t *testing.T) {
	fk := &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "23503",
		Message:        `update or delete on table "tiers" violates foreign key constraint "fk_tenants_tier" on table "tenants"`,
		Detail:         `Key (id)=(7) is still referenced from table "tenants".`,
		ConstraintName: "fk_tenants_tier",
	}
	v := integrity.All(fk)[0]
	const neutral = "NEUTRAL"

	t.Run("the driver text is replaced and the service's prefix kept", func(t *testing.T) {
		got, whole := integrity.Redact("delete tier: "+fk.Error(), v, neutral)
		assert.Equal(t, "delete tier: NEUTRAL", got)
		assert.False(t, whole)
	})
	for name, msg := range map[string]string{
		"the message":         "custom: " + fk.Message,
		"the constraint name": "index fk_tenants_tier refused it",
		"the detail":          "save failed: " + fk.Detail,
	} {
		t.Run(name+" on its own asks for the whole message to go", func(t *testing.T) {
			_, whole := integrity.Redact(msg, v, neutral)
			assert.True(t, whole)
		})
	}
	t.Run("a service's own sentence holding no fragment is kept", func(t *testing.T) {
		msg := "that tier is still in use"
		got, whole := integrity.Redact(msg, v, neutral)
		assert.Equal(t, msg, got)
		assert.False(t, whole)
	})
}

// The sentences are published strings, so they are spelled as literals here. Neither may
// contain a phrase a dcctl from before CONFLICT existed took for "already exists".
func TestTheNeutralSentencesNameNothingAnOlderClientWouldTakeAsSuccess(t *testing.T) {
	code, msg, ok := integrity.Answer(integrity.ClassReference)
	require.True(t, ok)
	assert.Equal(t, "REFERENCE_VIOLATION", code)
	assert.Equal(t, "the request refers to a record that does not exist, or removes one that other records still refer to", msg)

	code, msg, ok = integrity.Answer(integrity.ClassInvalid)
	require.True(t, ok)
	assert.Equal(t, "INVALID_VALUE", code)
	assert.Equal(t, "the request contains a value this record does not allow", msg)

	for _, s := range []string{integrity.MessageReference, integrity.MessageInvalid} {
		for _, phrase := range []string{"already exists", "duplicate", "unique"} {
			assert.NotContains(t, strings.ToLower(s), phrase)
		}
	}

	_, _, ok = integrity.Answer(integrity.ClassUnique)
	assert.False(t, ok, "core/conflict owns the unique answer")
	_, _, ok = integrity.Answer(integrity.Class(0))
	assert.False(t, ok)
	assert.Equal(t, "unknown", integrity.Class(99).String())
}

func TestNewRefusalRefusesAClassWithNoCode(t *testing.T) {
	assert.Panics(t, func() { integrity.NewRefusal(integrity.ClassUnique, "x") })
	assert.Panics(t, func() { integrity.NewRefusal(integrity.Class(0), "x") })
}

func TestARefusalCarriesItsCodeAndKeepsItsIdentity(t *testing.T) {
	r := integrity.NewRefusal(integrity.ClassReference, "entity is still referenced")
	assert.Equal(t, "entity is still referenced", r.Error())
	assert.Equal(t, map[string]any{"code": "REFERENCE_VIOLATION"}, r.Extensions())
	r.Extensions()["code"] = "MUTATED"
	assert.Equal(t, "REFERENCE_VIOLATION", r.Extensions()["code"], "each call returns a fresh map")
	assert.ErrorIs(t, fmt.Errorf("%w: 2 rows", r), r)

	inv := integrity.NewRefusal(integrity.ClassInvalid, "bad value")
	assert.Equal(t, map[string]any{"code": "INVALID_VALUE"}, inv.Extensions())
}
