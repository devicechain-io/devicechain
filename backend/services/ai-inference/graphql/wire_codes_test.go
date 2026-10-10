// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-microservice/aiwire"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wireCodeRoot struct{ err error }

func (r *wireCodeRoot) Probe() (string, error) { return "", r.err }

// The extensions.code each retryable refusal carries ON THE WIRE — after the tenant
// coarsening and through the GraphQL server — is the contract a caller classifies on.
// Pinned end to end because the code is served by the error's Extensions method, which a
// wrapped error does not expose: if tenantSafeError ever returned the wrapped error
// rather than the bare sentinel, the text would still read right and the code would
// silently vanish.
func TestRetryableRefusalsAreServedWithTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"timed out", fmt.Errorf("%w (after 40s): context deadline exceeded", inference.ErrTimedOut), aiwire.CodeTimedOut},
		{"rate limited", fmt.Errorf("gate: %w", inference.ErrRateLimited), aiwire.CodeRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := gql.MustParseSchema(`schema { query: Query } type Query { probe: String! }`,
				&wireCodeRoot{err: tenantSafeError(tc.err)})
			resp := schema.Exec(context.Background(), `{ probe }`, "", nil)
			raw, err := json.Marshal(resp)
			require.NoError(t, err)

			var body struct {
				Errors []struct {
					Message    string `json:"message"`
					Extensions struct {
						Code string `json:"code"`
					} `json:"extensions"`
				} `json:"errors"`
			}
			require.NoError(t, json.Unmarshal(raw, &body))
			require.Len(t, body.Errors, 1, "%s", raw)
			assert.Equal(t, tc.code, body.Errors[0].Extensions.Code, "%s", raw)
		})
	}
}
