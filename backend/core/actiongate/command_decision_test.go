// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

const alarmActuationGolden = `query AgentActuationDecision($kind: AgentActionKind!, $impact: AgentActionImpact, $targetCount: Int!) {
  agentActuationDecision(kind: $kind, impact: $impact, targetCount: $targetCount) {
    verdict requiredApprovals reasons policyVersion expiresInSeconds actionsPerHour clientAttested
  }
}`

const alarmExecutionGolden = `query AgentExecutionDecision($input: AgentExecutionDecisionInput!) {
  agentExecutionDecision(input: $input) {
    verdict requiredApprovals validApprovers reasons policyVersion actuationGeneration stepUpMaxAgeSeconds acceptRecoveryCodes allowSelfConfirm
  }
}`

func TestAlarmDecisionDocumentsUnchanged(t *testing.T) {
	for _, kind := range []Kind{KindAlarmAck, KindAlarmClear} {
		t.Run(string(kind), func(t *testing.T) {
			var posted struct {
				Query     string
				Variables json.RawMessage
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
				_, _ = io.WriteString(w, `{"data":{"agentActuationDecision":{"verdict":"APPROVE","requiredApprovals":1},"agentExecutionDecision":{"verdict":"APPROVE","requiredApprovals":1,"actuationGeneration":0}}}`)
			}))
			defer srv.Close()
			d := NewCallerDecider(srv.URL)
			ctx := auth.WithBearer(context.Background(), "caller")
			// Command context must never leak into an alarm document or its variables.
			var act ActuationRequest
			require.NoError(t, json.Unmarshal([]byte(`{"Kind":"`+string(kind)+`","Impact":"NORMAL","TargetCount":2,"Command":"reboot","GroupTokens":["g"]}`), &act))
			_, err := d.Actuation(ctx, act)
			require.NoError(t, err)
			require.Equal(t, alarmActuationGolden, posted.Query)
			require.JSONEq(t, `{"kind":"`+string(kind)+`","impact":"NORMAL","targetCount":2}`, string(posted.Variables))
			var ex ExecutionRequest
			require.NoError(t, json.Unmarshal([]byte(`{"Kind":"`+string(kind)+`","Impact":"NORMAL","TargetCount":2,"ProposalToken":"p","ProposerPrincipal":"a","ProposerClient":"c","ProposerSid":"s","Approvers":["b"],"Command":"reboot","GroupTokens":["g"]}`), &ex))
			_, err = d.Execution(ctx, ex)
			require.NoError(t, err)
			require.Equal(t, alarmExecutionGolden, posted.Query)
			require.JSONEq(t, `{"input":{"kind":"`+string(kind)+`","impact":"NORMAL","targetCount":2,"proposalToken":"p","proposerPrincipal":"a","proposerClient":"c","proposerSid":"s","approvers":["b"],"approverSessions":[]}}`, string(posted.Variables))
		})
	}
}

func TestCommandDecisionDocumentsAndRestrictions(t *testing.T) {
	for _, execution := range []bool{false, true} {
		for _, field := range []string{`null`, `[]`, `["g1","g2"]`} {
			t.Run(map[bool]string{false: "actuation", true: "execution"}[execution]+"/"+field, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, "Bearer caller", r.Header.Get("Authorization"))
					var posted struct {
						Query     string
						Variables map[string]any
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
					require.Contains(t, posted.Query, "groupRestriction")
					vars := posted.Variables
					root := "agentActuationDecision"
					if execution {
						root = "agentExecutionDecision"
						vars = vars["input"].(map[string]any)
					} else {
						require.Contains(t, posted.Query, "command: $command")
						require.Contains(t, posted.Query, "groupTokens: $groupTokens")
					}
					require.Equal(t, "reboot", vars["command"])
					require.Equal(t, []any{"g1"}, vars["groupTokens"])
					_, _ = io.WriteString(w, `{"data":{"`+root+`":{"verdict":"APPROVE","requiredApprovals":1,"actuationGeneration":0,"groupRestriction":`+field+`}}}`)
				}))
				defer srv.Close()
				d := NewCallerDecider(srv.URL)
				ctx := auth.WithBearer(context.Background(), "caller")
				raw := []byte(`{"Kind":"COMMAND_SEND","Command":"reboot","GroupTokens":["g1"],"TargetCount":1}`)
				var got any
				var err error
				if execution {
					var req ExecutionRequest
					require.NoError(t, json.Unmarshal(raw, &req))
					got, err = d.Execution(ctx, req)
				} else {
					var req ActuationRequest
					require.NoError(t, json.Unmarshal(raw, &req))
					got, err = d.Actuation(ctx, req)
				}
				require.NoError(t, err)
				encoded, err := json.Marshal(got)
				require.NoError(t, err)
				var result map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(encoded, &result))
				require.JSONEq(t, field, string(result["GroupRestriction"]))
			})
		}
	}
}

func TestCommandDecisionFailsClosed(t *testing.T) {
	for _, execution := range []bool{false, true} {
		for _, tc := range []struct {
			name, command, bearer string
			wantCalls             int
		}{
			{"missing restriction", "reboot", "caller", 1},
			{"empty command", "", "caller", 0},
			{"missing bearer", "reboot", "", 0},
		} {
			t.Run(map[bool]string{false: "actuation", true: "execution"}[execution]+"/"+tc.name, func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					_, _ = io.WriteString(w, `{"data":{"agentActuationDecision":{"verdict":"APPROVE","requiredApprovals":1},"agentExecutionDecision":{"verdict":"APPROVE","requiredApprovals":1,"actuationGeneration":0}}}`)
				}))
				defer srv.Close()
				ctx := context.Background()
				if tc.bearer != "" {
					ctx = auth.WithBearer(ctx, tc.bearer)
				}
				raw, err := json.Marshal(map[string]any{"Kind": "COMMAND_SEND", "Command": tc.command})
				require.NoError(t, err)
				d := NewCallerDecider(srv.URL)
				if execution {
					var req ExecutionRequest
					require.NoError(t, json.Unmarshal(raw, &req))
					_, err = d.Execution(ctx, req)
				} else {
					var req ActuationRequest
					require.NoError(t, json.Unmarshal(raw, &req))
					_, err = d.Actuation(ctx, req)
				}
				require.Error(t, err)
				require.Equal(t, tc.wantCalls, calls)
			})
		}
	}
}

func TestCommandSendIsSharedVocabulary(t *testing.T) {
	require.True(t, Kind("COMMAND_SEND").Valid())
}
