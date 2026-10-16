// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// This file uses only API that existed before FORBIDDEN did, so it compiles against
// that tree and fails there BY VALUE: the code was absent.

// The wire code, spelled as a literal so the test pins the published string.
const wireForbidden = "FORBIDDEN"

const forbiddenSDL = `
	schema { query: Query mutation: Mutation }
	type Query { ping: Boolean! }
	type Mutation {
		refused: Boolean!
		refusedWrapped: Boolean!
		refusedWhileMissing: Boolean!
		unauthenticated: Boolean!
	}
`

type forbiddenRoot struct{}

func (r *forbiddenRoot) Ping() bool { return true }

// Refused is how every resolver answers a caller without the authority: the gate's own
// sentinel, returned as is.
func (r *forbiddenRoot) Refused() (bool, error) { return false, auth.ErrForbidden }

func (r *forbiddenRoot) RefusedWrapped() (bool, error) {
	return false, fmt.Errorf("delete device: %w", auth.ErrForbidden)
}

// RefusedWhileMissing holds a refusal and a miss: the caller is told only that it may
// not, never whether the record exists.
func (r *forbiddenRoot) RefusedWhileMissing() (bool, error) {
	return false, errors.Join(gorm.ErrRecordNotFound, auth.ErrForbidden)
}

func (r *forbiddenRoot) Unauthenticated() (bool, error) { return false, auth.ErrUnauthenticated }

// 🔴 A REFUSAL CARRIES A CODE. auth.ErrForbidden used to reach the client as a bare
// message, so a client could tell "you may not" from "it failed" only by matching prose.
func TestForbiddenCarriesACode(t *testing.T) {
	s := MustParseSchema(forbiddenSDL, &forbiddenRoot{})

	code, msg := execOne(t, s, `mutation { refused }`)
	assert.Equal(t, wireForbidden, code)
	assert.Equal(t, "forbidden: missing required authority", msg, "the message is kept; only the code is added")

	code, msg = execOne(t, s, `mutation { refusedWrapped }`)
	assert.Equal(t, wireForbidden, code)
	assert.Equal(t, "delete device: forbidden: missing required authority", msg)

	// A refusal outranks a miss in the same chain: NOT_FOUND would confirm the record's
	// absence to a caller who may not ask.
	code, _ = execOne(t, s, `mutation { refusedWhileMissing }`)
	assert.Equal(t, wireForbidden, code)

	// Negative control: not being authenticated is not a refusal and gains no code.
	code, _ = execOne(t, s, `mutation { unauthenticated }`)
	assert.Nil(t, code)
}
