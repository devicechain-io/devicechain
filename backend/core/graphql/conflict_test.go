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

// These tests spell the wire code as the literal "CONFLICT" and the neutral sentence as
// a literal, not through the conflict package's constants, so they pin the published
// strings independently of the constants that produce them.
const (
	wireConflict   = "CONFLICT"
	neutralMessage = "the request conflicts with an existing record: a value that must be unique is already in use"
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
	}
	type Subscription { duplicate: Boolean! }
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

func (r *conflictRoot) ForeignKey() (bool, error) {
	fk := pgUniqueViolation()
	fk.Code, fk.Message = "23503", `insert or update on table "widgets" violates foreign key constraint "fk_x"`
	return false, fmt.Errorf("create widget: %w", fk)
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

// Controls: none of these is a uniqueness conflict answered by the boundary, and each
// passes through as it did before.
func TestExecLeavesWhatIsNotAConflictAlone(t *testing.T) {
	s := newConflictSchema(t)

	code, msg := execOne(t, s, `mutation { foreignKey }`)
	assert.Nil(t, code, "a foreign-key violation is not a uniqueness conflict")
	assert.Contains(t, msg, "SQLSTATE 23503")

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
// "code": the case answerConflicts must copy rather than write into.
type sharedExtensions struct {
	ext map[string]any
	err error
}

func (s sharedExtensions) Error() string                      { return s.err.Error() }
func (s sharedExtensions) Unwrap() error                      { return s.err }
func (s sharedExtensions) Extensions() map[string]interface{} { return s.ext }

// A typed error whose Extensions() returns a shared map without "code", over a driver
// unique violation: the answer gains CONFLICT, and the typed error's own map does NOT —
// otherwise every later request that reads that map would see a code nobody chose.
func TestAnswerConflictsCopiesATypedErrorsExtensionsRatherThanWritingIntoThem(t *testing.T) {
	shared := map[string]any{"hint": "retry"}
	typed := sharedExtensions{ext: shared, err: fmt.Errorf("create widget: %w", pgUniqueViolation())}
	qe := &gqlerrors.QueryError{Message: typed.Error(), ResolverError: typed, Extensions: typed.Extensions()}

	answerConflicts([]*gqlerrors.QueryError{qe})

	assert.Equal(t, wireConflict, qe.Extensions["code"])
	assert.Equal(t, "retry", qe.Extensions["hint"], "the typed error's own entries are kept")
	assert.Equal(t, map[string]any{"hint": "retry"}, shared, "the typed error's shared map must be left unmodified")
}
