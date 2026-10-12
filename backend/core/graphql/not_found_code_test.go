// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// The wire code, spelled as a literal so the test pins the published string rather than
// the constant that produces it.
const wireNotFound = "NOT_FOUND"

const notFoundSDL = `
	schema { query: Query mutation: Mutation }
	type Query { ping: Boolean! }
	type Mutation {
		missing: Boolean!
		missingBare: Boolean!
		missingTyped: Boolean!
		missingJoinedWithConflict: Boolean!
		otherError: Boolean!
	}
`

type notFoundRoot struct{}

func (r *notFoundRoot) Ping() bool { return true }

// Missing is how most services answer a miss: the store's own sentinel, wrapped.
func (r *notFoundRoot) Missing() (bool, error) {
	return false, fmt.Errorf("cancel batch: %w", gorm.ErrRecordNotFound)
}

// MissingBare is a resolver returning the sentinel itself, which several do.
func (r *notFoundRoot) MissingBare() (bool, error) { return false, gorm.ErrRecordNotFound }

// MissingTyped is a typed error that chose its own code for a miss.
func (r *notFoundRoot) MissingTyped() (bool, error) {
	return false, &rejected{msg: "no such widget", cause: gorm.ErrRecordNotFound}
}

// MissingJoinedWithConflict holds a miss and a unique violation: the integrity answer wins.
func (r *notFoundRoot) MissingJoinedWithConflict() (bool, error) {
	return false, errors.Join(gorm.ErrRecordNotFound, fmt.Errorf("create: %w", pgUniqueViolation()))
}

func (r *notFoundRoot) OtherError() (bool, error) { return false, errors.New("something else") }

// 🔴 A RECORD THAT DOES NOT EXIST CARRIES A CODE, WHICHEVER SERVICE ANSWERS. Most services
// report a miss as the store's own sentinel, and it used to reach the client as a bare
// "record not found" with no code — while other services answered the same miss with a code
// of their own — so a client could not branch on "it is not there" without matching prose.
func TestExecAnswersARecordThatDoesNotExistWithNotFound(t *testing.T) {
	s := MustParseSchema(notFoundSDL, &notFoundRoot{})

	code, msg := execOne(t, s, `mutation { missing }`)
	assert.Equal(t, wireNotFound, code)
	assert.Equal(t, "cancel batch: record not found", msg, "the message is the service's own; only the code is added")

	code, _ = execOne(t, s, `mutation { missingBare }`)
	assert.Equal(t, wireNotFound, code)

	// A typed error that chose its own code for the miss keeps it.
	code, msg = execOne(t, s, `mutation { missingTyped }`)
	assert.Equal(t, "REJECTED", code)
	assert.Equal(t, "no such widget", msg)

	// An integrity refusal in the same chain is the stronger statement.
	code, _ = execOne(t, s, `mutation { missingJoinedWithConflict }`)
	assert.Equal(t, wireConflict, code)

	// The counterweight: an error that is not a miss gains no code.
	code, msg = execOne(t, s, `mutation { otherError }`)
	assert.Nil(t, code)
	assert.Equal(t, "something else", msg)
}
