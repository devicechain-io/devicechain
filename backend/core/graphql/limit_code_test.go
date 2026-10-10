// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type limitRoot struct{}

func (limitRoot) Ping() bool { return true }

// Wrapped on purpose: graphql-go copies extensions only from the error returned
// directly, so the boundary has to find the refusal deeper in the chain.
func (limitRoot) Lookup() (bool, error) {
	return false, fmt.Errorf("devices by token: %w", limit.Exceeded("lookup keys", 1001, 1000))
}

func TestExecAnswersAWrappedLimitRefusalWithLimitExceeded(t *testing.T) {
	s := MustParseSchema(`
		schema { query: Query mutation: Mutation }
		type Query { ping: Boolean! }
		type Mutation { lookup: Boolean! }`, &limitRoot{})
	resp := s.Exec(context.Background(), `mutation { lookup }`, "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "LIMIT_EXCEEDED", resp.Errors[0].Extensions["code"])
	assert.Contains(t, resp.Errors[0].Message, "1001 requested, at most 1000 allowed")
}
