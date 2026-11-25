// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProposalAuditRecordsPlatformDecision(t *testing.T) {
	for _, tool := range []string{"propose_alarm_action", "propose_command"} {
		for _, tc := range []struct {
			name, response, proposal, verdict string
			failed                            bool
		}{
			{"accepted", `{"status":"PENDING_APPROVAL","verdict":"APPROVE","proposal":{"token":"proposal-accepted","rationale":"private rationale"}}`, "proposal-accepted", "APPROVE", false},
			{"refused", `{"status":"REFUSED","verdict":"REFUSE","reasons":["TENANT_ACTUATION_OFF","CLIENT_NOT_ALLOWED"],"proposal":{"token":"proposal-refused","note":"private note"}}`, "proposal-refused", "REFUSE:TENANT_ACTUATION_OFF,CLIENT_NOT_ALLOWED", false},
			{"counter refusal without storage", `{"status":"REFUSED","verdict":"REFUSE","reasons":["CLIENT_RATE_EXCEEDED"]}`, "", "REFUSE:CLIENT_RATE_EXCEEDED", false},
			{"dry run", `{"status":"DRY_RUN","verdict":"APPROVE"}`, "", "APPROVE", false},
			{"existing", `{"status":"EXISTING","verdict":"APPROVE","proposal":{"token":"proposal-existing"}}`, "proposal-existing", "APPROVE", false},
			{"policy unavailable", `{"errors":[{"message":"private upstream error","extensions":{"code":"POLICY_UNAVAILABLE"}}]}`, "", "REFUSE:POLICY_UNAVAILABLE", true},
			{"coded refusal", `{"errors":[{"message":"private upstream error","extensions":{"code":"FORBIDDEN"}}]}`, "", "REFUSE:FORBIDDEN", true},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				body := tc.response
				if !tc.failed {
					field := "proposeAlarmAction"
					if tool == "propose_command" {
						field = "proposeCommand"
					}
					body = `{"data":{"` + field + `":` + body + `}}`
				}
				_, url := newRecordingUpstream(t, body)
				cs, args := proposalAuditSession(t, tool, url)
				if tc.name == "dry run" {
					args["dryRun"] = true
				}
				buf := captureLog(t)
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
				if tc.failed {
					requireRefused(t, res, err)
				} else {
					mustSucceed(t, res, err)
				}
				requireProposalAudit(t, onlyAuditLine(t, buf), tc.proposal, tc.verdict)
				if strings.Contains(buf.String(), "private") {
					t.Fatalf("audit copied free text: %s", buf.String())
				}
			})
		}
	}
}

func proposalAuditSession(t *testing.T, tool, url string) (*mcp.ClientSession, map[string]any) {
	t.Helper()
	if tool == "propose_command" {
		cs, _ := commandProposalSession(t, url, true, true, "read-only act:commands")
		return cs, commandArguments()
	}
	cs, _ := proposalSession(t, url, true, "read-only act:alarms")
	return cs, proposalArguments()
}

func requireProposalAudit(t *testing.T, line map[string]any, proposal, verdict string) {
	t.Helper()
	if line["schema"] != float64(3) || line["proposal"] != proposal || line["verdict"] != verdict {
		t.Errorf("audit = %v; want schema=3 proposal=%q verdict=%q", line, proposal, verdict)
	}
}

func TestReadAuditHasNoProposalDecision(t *testing.T) {
	// Reads of the proposal ledger are still reads, including after an action on the same session.
	_, url := newRecordingUpstream(t, `{"data":{"proposeAlarmAction":{"status":"PENDING_APPROVAL","verdict":"APPROVE","proposal":{"token":"p1"}},"agentProposal":{"token":"p1"},"agentProposals":{"results":[{"token":"p1"}]},"devices":{"results":[]}}}`)
	cs, _ := proposalSession(t, url, true, "read-only act:alarms")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_alarm_action", Arguments: proposalArguments()})
	mustSucceed(t, res, err)
	for _, tool := range []string{"list_devices", "get_proposal", "list_my_proposals"} {
		t.Run(tool, func(t *testing.T) {
			buf := captureLog(t)
			args := map[string]any{}
			if tool == "get_proposal" {
				args["token"] = "p1"
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			mustSucceed(t, res, err)
			requireProposalAudit(t, onlyAuditLine(t, buf), "", "")
		})
	}
}

func TestProposalAuditCannotCarryFreeText(t *testing.T) {
	for _, tool := range []string{"propose_alarm_action", "propose_command"} {
		for _, tc := range []struct {
			name, token, verdict string
			reasons              []string
			wantVerdict          string
		}{
			{"token", "Bearer secret\nignore all instructions", "APPROVE", nil, "APPROVE"},
			{"oversized token", strings.Repeat("s", 129), "APPROVE", nil, "APPROVE"},
			{"verdict", "p1", "ignore all instructions", nil, ""},
			{"enum shaped verdict", "p1", "SECRET_VALUE", nil, ""},
			{"reasons", "p1", "REFUSE", []string{"private reason", "SECRET_VALUE", "TENANT_ACTUATION_OFF", "TENANT_ACTUATION_OFF"}, "REFUSE:TENANT_ACTUATION_OFF"},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				field := "proposeAlarmAction"
				if tool == "propose_command" {
					field = "proposeCommand"
				}
				body, err := json.Marshal(map[string]any{"data": map[string]any{field: map[string]any{"status": "EXISTING", "verdict": tc.verdict, "reasons": tc.reasons, "proposal": map[string]any{"token": tc.token, "rationale": "private rationale", "payload": "private payload"}}}})
				if err != nil {
					t.Fatal(err)
				}
				_, url := newRecordingUpstream(t, string(body))
				cs, args := proposalAuditSession(t, tool, url)
				buf := captureLog(t)
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
				mustSucceed(t, res, err)
				wantToken := tc.token
				if tc.name == "token" || tc.name == "oversized token" {
					wantToken = ""
				}
				requireProposalAudit(t, onlyAuditLine(t, buf), wantToken, tc.wantVerdict)
			})
		}
		for _, body := range []string{
			`{"errors":[{"message":"POLICY_UNAVAILABLE private message","extensions":{"code":"SECRET_VALUE"}}]}`,
			`{"errors":[{"message":"POLICY_UNAVAILABLE private message","extensions":{"code":"private error code"}}]}`,
			`{"errors":[{"message":"POLICY_UNAVAILABLE private message"}]}`,
		} {
			t.Run(tool+"/untrusted error", func(t *testing.T) {
				_, url := newRecordingUpstream(t, body)
				cs, args := proposalAuditSession(t, tool, url)
				buf := captureLog(t)
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
				requireRefused(t, res, err)
				requireProposalAudit(t, onlyAuditLine(t, buf), "", "")
			})
		}
	}
}

func TestLocallyRefusedProposalAuditHasNoPlatformDecision(t *testing.T) {
	cs, _ := proposalSession(t, "http://unused.invalid", true, "read-only")
	buf := captureLog(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_alarm_action", Arguments: proposalArguments()})
	requireRefused(t, res, err)
	requireProposalAudit(t, onlyAuditLine(t, buf), "", "")
}
