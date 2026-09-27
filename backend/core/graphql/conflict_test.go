// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/gorilla/websocket"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests spell the wire codes and the neutral sentences as literals, not through
// the conflict and integrity packages' constants, so they pin the published strings
// independently of the constants that produce them.
const (
	wireConflict     = "CONFLICT"
	neutralMessage   = "the request conflicts with an existing record: a value that must be unique is already in use"
	wireReference    = "REFERENCE_VIOLATION"
	neutralReference = "the request refers to a record that does not exist, or removes one that other records still refer to"
	wireInvalid      = "INVALID_VALUE"
	neutralInvalid   = "the request contains a value this record does not allow"
)

const conflictSDL = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query { ping: Boolean! }
	type Mutation {
		pgDuplicate: Boolean!
		foreignKey: Boolean!
		proseOnly: Boolean!
		rejectedPrinting: Boolean!
		rejectedOwnSentence: Boolean!
		fragmentOnly: Boolean!
		throttled: Boolean!
		checkViolation: Boolean!
		notNull: Boolean!
		fkDetailOnly: Boolean!
		rejectedPrintingFK: Boolean!
		fkJoinedWithConflict: Boolean!
		conflictJoinedWithFK: Boolean!
		uniqueFragmentJoinedWithFK: Boolean!
		conflictJoinedWithCheck: Boolean!
		serialization: Boolean!
		fkProse: Boolean!
	}
	type Subscription { duplicate: Boolean! dangling: Boolean! }
`

// pgUniqueViolation is the error pgx hands back for a unique violation, as it arrives.
func pgUniqueViolation() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "23505",
		Message:        `duplicate key value violates unique constraint "uix_widgets_tenant_token"`,
		ConstraintName: "uix_widgets_tenant_token",
	}
}

// conflictRoot's resolvers return driver errors as a service would, wrapped. A REAL
// SQLite duplicate through a served schema is driven by device-management's
// conflict_wire_test.go; this package's tests import no database driver, because every
// module that links core/graphql would otherwise carry that driver in its module graph.
type conflictRoot struct{}

func (r *conflictRoot) Ping() bool { return true }

func (r *conflictRoot) PgDuplicate() (bool, error) {
	return false, fmt.Errorf("create widget: %w", pgUniqueViolation())
}

// pgForeignKeyViolation is the error pgx hands back for a foreign-key violation. Its
// Detail repeats the value sent, which must not survive either.
func pgForeignKeyViolation() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "23503",
		Message:        `insert or update on table "widgets" violates foreign key constraint "fk_widgets_tier"`,
		Detail:         `Key (tier_id)=(7) is not present in table "tiers".`,
		ConstraintName: "fk_widgets_tier",
		TableName:      "widgets",
	}
}

func (r *conflictRoot) ForeignKey() (bool, error) {
	return false, fmt.Errorf("create widget: %w", pgForeignKeyViolation())
}

func (r *conflictRoot) CheckViolation() (bool, error) {
	return false, fmt.Errorf("create widget: %w", &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "23514",
		Message:        `new row for relation "widgets" violates check constraint "ck_widgets_qty"`,
		Detail:         "Failing row contains (7, -1).",
		ConstraintName: "ck_widgets_qty",
		TableName:      "widgets",
	})
}

func (r *conflictRoot) NotNull() (bool, error) {
	return false, fmt.Errorf("create widget: %w", &pgconn.PgError{
		Severity:   "ERROR",
		Code:       "23502",
		Message:    `null value in column "name" of relation "widgets" violates not-null constraint`,
		Detail:     "Failing row contains (7, null).",
		TableName:  "widgets",
		ColumnName: "name",
	})
}

// FkDetailOnly prints only the Detail of the driver error — the part that repeats the
// value sent.
func (r *conflictRoot) FkDetailOnly() (bool, error) {
	fk := pgForeignKeyViolation()
	return false, &wrapped{msg: "save failed: " + fk.Detail, cause: fk}
}

func (r *conflictRoot) RejectedPrintingFK() (bool, error) {
	fk := pgForeignKeyViolation()
	return false, &rejected{msg: "rejected: " + fk.Error(), cause: fk}
}

func (r *conflictRoot) FkJoinedWithConflict() (bool, error) {
	return false, fmt.Errorf("%w; %w", pgForeignKeyViolation(), pgUniqueViolation())
}

func (r *conflictRoot) ConflictJoinedWithFK() (bool, error) {
	return false, fmt.Errorf("%w; %w", pgUniqueViolation(), pgForeignKeyViolation())
}

// UniqueFragmentJoinedWithFK prints a fragment of the unique violation over a chain that
// also holds a foreign-key violation: the code served is REFERENCE_VIOLATION, so the whole
// message is replaced by THAT code's sentence, not the unique violation's own.
func (r *conflictRoot) UniqueFragmentJoinedWithFK() (bool, error) {
	unique := pgUniqueViolation()
	return false, &wrapped{msg: "save failed: " + unique.Message, cause: errors.Join(unique, pgForeignKeyViolation())}
}

func (r *conflictRoot) ConflictJoinedWithCheck() (bool, error) {
	_, err := r.CheckViolation()
	return false, fmt.Errorf("%w; %w", pgUniqueViolation(), err)
}

func (r *conflictRoot) Serialization() (bool, error) {
	return false, fmt.Errorf("save: %w", &pgconn.PgError{
		Severity: "ERROR", Code: "40001", Message: "could not serialize access due to concurrent update",
	})
}

func (r *conflictRoot) FkProse() (bool, error) {
	return false, errors.New(`insert or update on table "widgets" violates foreign key constraint "x" (SQLSTATE 23503)`)
}

func (r *conflictRoot) Dangling(context.Context) (<-chan bool, error) {
	return nil, fmt.Errorf("watch widget: %w", pgForeignKeyViolation())
}

func (r *conflictRoot) ProseOnly() (bool, error) {
	return false, errors.New(`duplicate key value violates unique constraint "x" (SQLSTATE 23505)`)
}

// rejected is a typed error that already chose its own code.
type rejected struct {
	msg   string
	cause error
}

func (e *rejected) Error() string              { return e.msg }
func (e *rejected) Unwrap() error              { return e.cause }
func (e *rejected) Extensions() map[string]any { return map[string]any{"code": "REJECTED"} }

func (r *conflictRoot) RejectedPrinting() (bool, error) {
	pg := pgUniqueViolation()
	return false, &rejected{msg: "rejected: " + pg.Error(), cause: pg}
}

func (r *conflictRoot) RejectedOwnSentence() (bool, error) {
	return false, &rejected{msg: "rejected for its own reasons", cause: pgUniqueViolation()}
}

// FragmentOnly prints part of the driver error (its Message) without the whole text.
func (r *conflictRoot) FragmentOnly() (bool, error) {
	pg := pgUniqueViolation()
	return false, &wrapped{msg: "save failed: " + pg.Message, cause: pg}
}

type wrapped struct {
	msg   string
	cause error
}

func (e *wrapped) Error() string { return e.msg }
func (e *wrapped) Unwrap() error { return e.cause }

func (r *conflictRoot) Throttled() (bool, error) {
	return false, &credential.ThrottledError{RetryAfter: 2 * time.Second}
}

func (r *conflictRoot) Duplicate(context.Context) (<-chan bool, error) {
	return nil, fmt.Errorf("watch widget: %w", pgUniqueViolation())
}

func newConflictSchema(t *testing.T) *Schema {
	t.Helper()
	return MustParseSchema(conflictSDL, &conflictRoot{})
}

func execOne(t *testing.T, s *Schema, query string) (code any, message string) {
	t.Helper()
	resp := s.Exec(context.Background(), query, "", nil)
	require.Len(t, resp.Errors, 1, "expected exactly one error for %s", query)
	return resp.Errors[0].Extensions["code"], resp.Errors[0].Message
}

// A Postgres duplicate, returned wrapped, is answered with the code and without the
// database's wording, keeping the service's own prefix.
func TestExecAnswersAPostgresDuplicateWithConflict(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { pgDuplicate }`)
	assert.Equal(t, wireConflict, code)
	assert.Equal(t, "create widget: "+neutralMessage, msg)
	assert.NotContains(t, msg, "SQLSTATE")
	assert.NotContains(t, msg, "uix_")
}

// A fragment of the driver error printed without its full text still leaks, so the
// whole message is replaced.
func TestExecReplacesAMessageCarryingOnlyAFragmentOfTheDriverError(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { fragmentOnly }`)
	assert.Equal(t, wireConflict, code)
	assert.Equal(t, neutralMessage, msg)
}

// Controls: none of these is an integrity refusal answered by the boundary, and each
// passes through as it did before.
func TestExecLeavesWhatIsNotAnIntegrityRefusalAlone(t *testing.T) {
	s := newConflictSchema(t)

	code, msg := execOne(t, s, `mutation { serialization }`)
	assert.Nil(t, code, "a serialization failure is not an integrity violation")
	assert.Equal(t, "save: ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)", msg)

	code, msg = execOne(t, s, `mutation { fkProse }`)
	assert.Nil(t, code, "text that reads like a foreign-key violation is not one: the TYPE is matched")
	assert.Equal(t, `insert or update on table "widgets" violates foreign key constraint "x" (SQLSTATE 23503)`, msg)

	code, msg = execOne(t, s, `mutation { proseOnly }`)
	assert.Nil(t, code, "text that reads like a violation is not one: the TYPE is matched")
	assert.Equal(t, `duplicate key value violates unique constraint "x" (SQLSTATE 23505)`, msg)

	code, msg = execOne(t, s, `mutation { throttled }`)
	assert.Equal(t, credential.CodeThrottled, code)
	assert.Equal(t, "too many failed sign-in attempts; try again in 2 seconds", msg)
}

// A typed error that already chose a code keeps it. The driver text it printed is still
// removed; a sentence of its own is kept.
func TestExecKeepsACodeATypedErrorAlreadyChose(t *testing.T) {
	s := newConflictSchema(t)

	code, msg := execOne(t, s, `mutation { rejectedPrinting }`)
	assert.Equal(t, "REJECTED", code)
	assert.Equal(t, "rejected: "+neutralMessage, msg)

	code, msg = execOne(t, s, `mutation { rejectedOwnSentence }`)
	assert.Equal(t, "REJECTED", code)
	assert.Equal(t, "rejected for its own reasons", msg)
}

// The WebSocket pump serves subscription responses without going through Exec, so it
// answers conflicts itself. A subscription whose resolver refuses with a wrapped driver
// violation reaches the client with the code and without the database's wording.
func TestTheSubscriptionPumpAnswersAConflictWithConflict(t *testing.T) {
	h := NewSubscriptionHandler(newConflictSchema(t), map[ContextKey]interface{}{}, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)
	writeMsg(t, conn, subscribeMsg("1", "subscription { duplicate }", nil))

	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "payload: %s", msg.Payload)
	var body struct {
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(msg.Payload, &body))
	require.Len(t, body.Errors, 1)
	assert.Equal(t, wireConflict, body.Errors[0].Extensions["code"])
	assert.Equal(t, "watch widget: "+neutralMessage, body.Errors[0].Message)
}

// sharedExtensions is an Extensions() that hands out ONE map to every caller and sets no
// "code": the case answerIntegrity must copy rather than write into.
type sharedExtensions struct {
	ext map[string]any
	err error
}

func (s sharedExtensions) Error() string                      { return s.err.Error() }
func (s sharedExtensions) Unwrap() error                      { return s.err }
func (s sharedExtensions) Extensions() map[string]interface{} { return s.ext }

// A typed error whose Extensions() returns a shared map without "code", over a driver
// violation: the answer gains the code, and the typed error's own map does NOT —
// otherwise every later request that reads that map would see a code nobody chose.
func TestAnswerIntegrityCopiesATypedErrorsExtensionsRatherThanWritingIntoThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		drv  error
		want string
	}{
		{"unique", pgUniqueViolation(), wireConflict},
		{"foreign key", pgForeignKeyViolation(), wireReference},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared := map[string]any{"hint": "retry"}
			typed := sharedExtensions{ext: shared, err: fmt.Errorf("create widget: %w", tc.drv)}
			qe := &gqlerrors.QueryError{Message: typed.Error(), ResolverError: typed, Extensions: typed.Extensions()}

			answerIntegrity([]*gqlerrors.QueryError{qe})

			assert.Equal(t, tc.want, qe.Extensions["code"])
			assert.Equal(t, "retry", qe.Extensions["hint"], "the typed error's own entries are kept")
			assert.Equal(t, map[string]any{"hint": "retry"}, shared, "the typed error's shared map must be left unmodified")
		})
	}
}

// A foreign-key violation, returned wrapped, is answered with REFERENCE_VIOLATION and a
// neutral sentence in place of the database's wording, keeping the service's prefix.
func TestExecAnswersAForeignKeyViolationWithReferenceViolation(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { foreignKey }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, "create widget: "+neutralReference, msg)
}

// A check violation is answered with INVALID_VALUE; the value it repeated in its detail
// does not survive.
func TestExecAnswersACheckViolationWithInvalidValue(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { checkViolation }`)
	assert.Equal(t, wireInvalid, code)
	assert.Equal(t, "create widget: "+neutralInvalid, msg)
}

func TestExecAnswersANotNullViolationWithInvalidValue(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { notNull }`)
	assert.Equal(t, wireInvalid, code)
	assert.Equal(t, "create widget: "+neutralInvalid, msg)
}

// A message carrying only the DETAIL of a foreign-key violation — the part that repeats
// the value sent — is replaced whole.
func TestExecReplacesAMessageCarryingOnlyTheDetailOfAForeignKeyViolation(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { fkDetailOnly }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, neutralReference, msg)
}

// A typed error that already chose its code keeps it over a foreign-key violation, and
// the driver text it printed is still removed.
func TestExecKeepsACodeATypedErrorAlreadyChoseOverAForeignKeyViolation(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { rejectedPrintingFK }`)
	assert.Equal(t, "REJECTED", code)
	assert.Equal(t, "rejected: "+neutralReference, msg)
}

// A chain holding a unique violation AND a foreign-key (or check) violation is not "already exists,
// carry on": re-running it cannot succeed, so it is never answered CONFLICT, whichever
// comes first in the chain. Both driver texts are removed, and neither is replaced by
// the unique sentence: it says "unique", which a client from before CONFLICT existed
// reads as "already exists", so it is never served under REFERENCE_VIOLATION.
func TestAChainHoldingAForeignKeyViolationIsNeverAConflict(t *testing.T) {
	s := newConflictSchema(t)

	code, msg := execOne(t, s, `mutation { fkJoinedWithConflict }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, neutralReference+"; "+neutralReference, msg)

	code, msg = execOne(t, s, `mutation { conflictJoinedWithFK }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, neutralReference+"; "+neutralReference, msg)

	code, msg = execOne(t, s, `mutation { conflictJoinedWithCheck }`)
	assert.Equal(t, wireInvalid, code)
	assert.Equal(t, neutralInvalid+"; create widget: "+neutralInvalid, msg)
}

// A message replaced WHOLE takes the sentence of the code actually served, not of the
// violation whose fragment triggered it: a unique fragment over a chain served
// REFERENCE_VIOLATION must not read "a value that must be unique is already in use".
func TestAWholeReplacementTakesTheSentenceOfTheCodeServed(t *testing.T) {
	code, msg := execOne(t, newConflictSchema(t), `mutation { uniqueFragmentJoinedWithFK }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, neutralReference, msg)
}

// The subscription pump answers a foreign-key violation the way Exec does.
func TestTheSubscriptionPumpAnswersAForeignKeyViolation(t *testing.T) {
	h := NewSubscriptionHandler(newConflictSchema(t), map[ContextKey]interface{}{}, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)
	writeMsg(t, conn, subscribeMsg("1", "subscription { dangling }", nil))

	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "payload: %s", msg.Payload)
	var body struct {
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(msg.Payload, &body))
	require.Len(t, body.Errors, 1)
	assert.Equal(t, wireReference, body.Errors[0].Extensions["code"])
	assert.Equal(t, "watch widget: "+neutralReference, body.Errors[0].Message)
}

// The client no longer sees the database's wording, so the operator must: a foreign-key
// or check violation is logged at WARN (every protected reference has a check before
// the write, so the database refusing one is a lost race or a server bug), naming the
// constraint and never the detail, which repeats the values sent. A unique violation,
// routine, is logged at DEBUG.
func TestAnIntegrityViolationIsLoggedAtItsLevelWithoutItsDetail(t *testing.T) {
	s := newConflictSchema(t)
	type want struct{ level, class, constraint, table string }
	var (
		reference = want{"warn", "reference", "fk_widgets_tier", "widgets"}
		invalid   = want{"warn", "invalid", "ck_widgets_qty", "widgets"}
		unique    = want{"debug", "unique", "uix_widgets_tenant_token", ""}
	)
	for _, tc := range []struct {
		name, query string
		want        []want
		leaks       []string
	}{
		{"reference", `mutation { foreignKey }`, []want{reference}, []string{"tier_id"}},
		{"invalid", `mutation { checkViolation }`, []want{invalid}, []string{"Failing row"}},
		{"unique", `mutation { pgDuplicate }`, []want{unique}, []string{"duplicate key"}},
		// A typed error that chose its own code does not hide the violation beneath it.
		{"under a typed code", `mutation { rejectedPrintingFK }`, []want{reference}, []string{"tier_id"}},
		// Every violation in a chain is logged, not only the first.
		{"joined chain", `mutation { conflictJoinedWithFK }`, []want{unique, reference}, []string{"tier_id", "duplicate key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := logSink.Capture(t)
			execOne(t, s, tc.query)
			lines := map[string]map[string]any{}
			for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(raw), &m) == nil {
					if class, ok := m["class"].(string); ok {
						lines[class] = m
					}
				}
			}
			assert.Len(t, lines, len(tc.want), "captured: %s", logs.String())
			for _, w := range tc.want {
				line := lines[w.class]
				require.NotNil(t, line, "no log line for the %s violation; captured: %s", w.class, logs.String())
				assert.Equal(t, w.level, line["level"])
				assert.Equal(t, w.constraint, line["constraint"])
				assert.Equal(t, w.table, line["table"])
			}
			for _, leak := range tc.leaks {
				assert.NotContains(t, logs.String(), leak)
			}
		})
	}
}
