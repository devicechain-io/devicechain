// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package conflict_test

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pgErr(code, constraint string) *pgconn.PgError {
	return &pgconn.PgError{
		Severity:       "ERROR",
		Code:           code,
		Message:        fmt.Sprintf(`duplicate key value violates unique constraint "%s"`, constraint),
		ConstraintName: constraint,
	}
}

// coded mimics the SQLite driver's error shape from the WRONG package: a Code() of 2067
// alone must not make an error a conflict. (The tests over a REAL SQLite error live in
// core/rdb, so that this package's tests do not pull a database driver into the module
// graph of every consumer — dcctl included.)
type coded struct{}

func (coded) Error() string { return "UNIQUE constraint failed: x.y (2067)" }
func (coded) Code() int     { return 2067 }

func TestAsRecognisesAPostgresUniqueViolationThroughAnyWrapping(t *testing.T) {
	pg := pgErr("23505", "uix_a")
	for name, err := range map[string]error{
		"bare":           pg,
		"single-wrapped": fmt.Errorf("save: %w", pg),
		"double-wrapped": fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pg)),
		"joined":         errors.Join(io.EOF, pg),
	} {
		t.Run(name, func(t *testing.T) {
			c, ok := conflict.As(err)
			require.True(t, ok)
			assert.Equal(t, "uix_a", c.Constraint)
			assert.Equal(t, conflict.Message, c.Error(), "a recognised violation must not carry driver text")
			assert.Same(t, pg, c.Unwrap())
			assert.Equal(t, map[string]any{"code": "CONFLICT"}, c.Extensions())
		})
	}
}

func TestAServiceRefusalIsAConflictWithItsOwnSentence(t *testing.T) {
	c, ok := conflict.As(fmt.Errorf("ctx: %w", conflict.New("x")))
	require.True(t, ok)
	assert.Equal(t, "x", c.Error())
	assert.Nil(t, c.Unwrap())
	assert.Equal(t, `token "a" is taken`, conflict.Errorf("token %q is taken", "a").Error())
}

// The service's own *Error is its account of the refusal and wins over the driver
// error beneath it.
func TestAnOuterServiceRefusalWinsOverTheDriverError(t *testing.T) {
	outer := &wrapping{msg: "renamed onto a taken token", cause: pgErr("23505", "uix_b")}
	own := conflict.New("mine")
	c, ok := conflict.As(fmt.Errorf("%w: %w", own, outer))
	require.True(t, ok)
	assert.Same(t, own, c)
}

type wrapping struct {
	msg   string
	cause error
}

func (w *wrapping) Error() string { return w.msg }
func (w *wrapping) Unwrap() error { return w.cause }

func TestWhatIsNotAConflict(t *testing.T) {
	for name, err := range map[string]error{
		"nil":                   nil,
		"foreign key 23503":     fmt.Errorf("w: %w", pgErr("23503", "fk_a")),
		"check 23514":           fmt.Errorf("w: %w", pgErr("23514", "ck_a")),
		"23505 as prose only":   errors.New(`ERROR: duplicate key value violates unique constraint "x" (SQLSTATE 23505)`),
		"sqlite prose only":     errors.New("constraint failed: UNIQUE constraint failed: t.c (2067)"),
		"Code() 2067 elsewhere": fmt.Errorf("w: %w", coded{}),
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, conflict.Is(err))
			_, _, changed := conflict.Redact("anything", err)
			assert.False(t, changed)
		})
	}
}

func TestRedact(t *testing.T) {
	pg := pgErr("23505", "uix_widgets_tenant_token")
	full := pg.Error()

	t.Run("the driver text is replaced and the service's prefix kept", func(t *testing.T) {
		got, constraint, changed := conflict.Redact("create widget: "+full, fmt.Errorf("create widget: %w", pg))
		assert.Equal(t, "create widget: "+conflict.Message, got)
		assert.Equal(t, "uix_widgets_tenant_token", constraint)
		assert.True(t, changed)
	})

	t.Run("a fragment printed without the full text replaces the whole message", func(t *testing.T) {
		msg := "custom: " + pg.Message
		got, _, changed := conflict.Redact(msg, &wrapping{msg: msg, cause: pg})
		assert.Equal(t, conflict.Message, got)
		assert.True(t, changed)
	})

	t.Run("a constraint name on its own replaces the whole message", func(t *testing.T) {
		msg := "index uix_widgets_tenant_token refused it"
		got, _, _ := conflict.Redact(msg, &wrapping{msg: msg, cause: pg})
		assert.Equal(t, conflict.Message, got)
	})

	t.Run("a service's own sentence holding no fragment is kept", func(t *testing.T) {
		msg := "that token is already in use"
		got, _, changed := conflict.Redact(msg, &wrapping{msg: msg, cause: pg})
		assert.Equal(t, msg, got)
		assert.False(t, changed)
	})

	// A service refusal joined with the driver error: the *Error wins As, but the
	// driver's text is still found and removed.
	t.Run("a service conflict cannot shield a driver error beside it", func(t *testing.T) {
		err := errors.Join(conflict.New("taken"), pg)
		c, ok := conflict.As(err)
		require.True(t, ok)
		assert.Equal(t, "taken", c.Error())
		got, _, changed := conflict.Redact(err.Error(), err)
		assert.Equal(t, "taken\n"+conflict.Message, got)
		assert.True(t, changed)
	})
}
