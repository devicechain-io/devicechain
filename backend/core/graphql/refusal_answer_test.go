// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/stretchr/testify/assert"
)

// A service's own "still referenced" refusal, answered at the boundary. The code is the
// one a database foreign-key violation gets, so a client sees ONE code whether the
// service's check refused the delete or the database did after a concurrent change.

const refusalSDL = `
	schema { query: Query mutation: Mutation }
	type Query { ping: Boolean! }
	type Mutation {
		refusalDirect: Boolean!
		refusalWrapped: Boolean!
		serviceConflictJoinedWithRefusal: Boolean!
		refusalJoinedWithServiceConflict: Boolean!
	}
`

var errStillReferenced = integrity.NewRefusal(integrity.ClassReference, "widget is still referenced")

type refusalRoot struct{}

func (*refusalRoot) Ping() bool { return true }

// RefusalDirect returns the sentinel unwrapped: graphql-go reads its Extensions itself.
func (*refusalRoot) RefusalDirect() (bool, error) { return false, errStillReferenced }

// RefusalWrapped wraps it the way the services do: only the boundary can find it.
func (*refusalRoot) RefusalWrapped() (bool, error) {
	return false, fmt.Errorf("%w: 2 gadget(s) reference widget %q", errStillReferenced, "w1")
}

func (*refusalRoot) ServiceConflictJoinedWithRefusal() (bool, error) {
	return false, fmt.Errorf("%w; %w", conflict.New("token is taken"), errStillReferenced)
}

func (*refusalRoot) RefusalJoinedWithServiceConflict() (bool, error) {
	return false, fmt.Errorf("%w; %w", errStillReferenced, conflict.New("token is taken"))
}

func TestAServiceRefusalCarriesReferenceViolationWrappedOrNot(t *testing.T) {
	s := MustParseSchema(refusalSDL, &refusalRoot{})

	code, msg := execOne(t, s, `mutation { refusalDirect }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, "widget is still referenced", msg)

	code, msg = execOne(t, s, `mutation { refusalWrapped }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, `widget is still referenced: 2 gadget(s) reference widget "w1"`, msg,
		"the service's own sentence is kept")
}

// A service conflict beside a service refusal is not "already exists, carry on",
// whichever comes first in the chain.
func TestAServiceConflictBesideARefusalIsNotAConflict(t *testing.T) {
	s := MustParseSchema(refusalSDL, &refusalRoot{})

	code, msg := execOne(t, s, `mutation { serviceConflictJoinedWithRefusal }`)
	assert.Equal(t, wireReference, code)
	assert.Equal(t, "token is taken; widget is still referenced", msg)

	code, _ = execOne(t, s, `mutation { refusalJoinedWithServiceConflict }`)
	assert.Equal(t, wireReference, code)
}
