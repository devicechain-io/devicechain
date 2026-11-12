// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestExecutionDecisionRequiresActuationGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		valid       bool
		generation  int
	}{
		{name: "missing"},
		{name: "null", field: `,"actuationGeneration":null`},
		{name: "negative", field: `,"actuationGeneration":-1`},
		{name: "initial", field: `,"actuationGeneration":0`, valid: true},
		{name: "restored", field: `,"actuationGeneration":9`, valid: true, generation: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Contains(t, string(b), "actuationGeneration")
				_, _ = io.WriteString(w, `{"data":{"agentExecutionDecision":{"verdict":"APPROVE","requiredApprovals":1,"policyVersion":4`+tc.field+`}}}`)
			}))
			defer srv.Close()
			d, err := NewCallerDecider(srv.URL).Execution(auth.WithBearer(context.Background(), "person"), ExecutionRequest{Kind: KindAlarmAck})
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.generation, d.ActuationGeneration)
			require.Equal(t, 4, d.PolicyVersion)
		})
	}
}
