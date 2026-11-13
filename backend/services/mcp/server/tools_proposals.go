// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"regexp"
	"unicode"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// alarmTokenPattern bounds what an agent may put in a target. A refused proposal is stored with
// its targets even when no such alarm exists, so a free-form string here would come back to the
// model, unmarked, as an identifier.
var alarmTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

const proposalNotice = "Preview. A person must approve in the console Approvals inbox; NOTHING happens until a person approves it in the console. An agent can never approve."

func registerProposals(s *mcp.Server, c *Catalog, t *Tools, enabled bool) {
	registerAction(s, c, &mcp.Tool{Name: "propose_alarm_action", InputSchema: proposalInputSchema(), Description: proposalNotice + " Propose acknowledging or clearing 1 to 100 alarms. Supply your own requestId idempotency key unchanged on retries; dryRun checks without storing."}, []string{coreauth.ScopeActAlarms}, ActionRisk{Impact: ImpactNormal, Reversible: false, Discloses: "stores a request for people to review, including alarm targets and the agent's rationale"}, enabled, t.ProposeAlarmAction)
	registerAction(s, c, &mcp.Tool{Name: "get_proposal", Description: proposalNotice + " Read one proposal by token."}, nil, ActionRisk{Impact: ImpactNormal, Reversible: true, ReadOnly: true, Discloses: "reveals a proposal's targets, author and approval state"}, enabled, t.GetProposal)
	registerAction(s, c, &mcp.Tool{Name: "list_my_proposals", InputSchema: proposalListSchema(), Description: proposalNotice + " List your own proposals, paged and optionally filtered by state and kind."}, nil, ActionRisk{Impact: ImpactNormal, Reversible: true, ReadOnly: true, Discloses: "reveals your own proposals and their approval states a page at a time"}, enabled, t.ListMyProposals)
}

type ProposeAlarmActionInput struct {
	Action      string   `json:"action" jsonschema:"ack or clear"`
	AlarmTokens []string `json:"alarmTokens" jsonschema:"1 to 100 alarm tokens"`
	RequestID   string   `json:"requestId" jsonschema:"caller supplied idempotency key; 1 to 128 printable characters; never changed"`
	Rationale   *string  `json:"rationale,omitempty"`
	DryRun      *bool    `json:"dryRun,omitempty"`
}
type AlarmProposalPreview struct {
	AlarmToken         string `json:"alarmToken" prov:"id"`
	State              string `json:"state" prov:"platform"`
	Acknowledged       bool   `json:"acknowledged"`
	HidesLiveCondition bool   `json:"hidesLiveCondition"`
}
type ProposalUntrusted struct {
	Rationale    *Marked   `json:"rationale,omitempty" prov:"agent"`
	Note         *Marked   `json:"note,omitempty" prov:"operator"`
	OtherTargets []*Marked `json:"otherTargets,omitempty" prov:"device-reported"`
}
type ProposalApproval struct {
	Principal  string `json:"principal" prov:"id"`
	ApprovedAt string `json:"approvedAt" prov:"platform"`
}
type Proposal struct {
	Token             string             `json:"token" prov:"id"`
	Kind              string             `json:"kind" prov:"platform"`
	State             string             `json:"state" prov:"platform"`
	Targets           []string           `json:"targets" prov:"id"`
	TargetsNote       string             `json:"targetsNote,omitempty" prov:"platform"`
	Digest            string             `json:"digest" prov:"platform"`
	Impact            string             `json:"impact" prov:"platform"`
	TargetCount       int                `json:"targetCount"`
	ProposerPrincipal string             `json:"proposerPrincipal" prov:"id"`
	ProposerClient    string             `json:"proposerClient" prov:"id"`
	ClientAttested    bool               `json:"clientAttested"`
	PolicyVersion     int                `json:"policyVersion"`
	RequiredApprovals int                `json:"requiredApprovals"`
	Approvals         []ProposalApproval `json:"approvals"`
	Reasons           []string           `json:"reasons" prov:"platform"`
	CreatedAt         string             `json:"createdAt" prov:"platform"`
	ExpiresAt         string             `json:"expiresAt" prov:"platform"`
	DecidedAt         *string            `json:"decidedAt,omitempty" prov:"platform"`
	DecidedBy         *string            `json:"decidedBy,omitempty" prov:"id"`
	Untrusted         *ProposalUntrusted `json:"untrusted,omitempty"`
}
type proposalResponse struct {
	Proposal
	Rationale string `json:"rationale"`
	Note      string `json:"note"`
}

func (p proposalResponse) output() Proposal {
	out := p.Proposal
	out.Untrusted = &ProposalUntrusted{Rationale: mark(SourceAgent, p.Rationale), Note: mark(SourceOperator, p.Note)}
	out.Targets = make([]string, 0, len(p.Targets))
	for _, target := range p.Targets {
		if alarmTokenPattern.MatchString(target) {
			out.Targets = append(out.Targets, target)
			continue
		}
		// Stored targets may bypass MCP input validation. Preserve even empty invalid
		// values as untrusted text, using the least-trusting source class.
		out.Untrusted.OtherTargets = append(out.Untrusted.OtherTargets, &Marked{Value: target, Source: SourceDevice})
	}
	if len(out.Untrusted.OtherTargets) > 0 {
		out.TargetsNote = "some stored targets are not alarm tokens and are shown as untrusted text"
	}
	return out
}

type ProposalOutcome struct {
	Status            string                 `json:"status" prov:"platform"`
	Verdict           string                 `json:"verdict" prov:"platform"`
	RequiredApprovals int                    `json:"requiredApprovals"`
	Reasons           []string               `json:"reasons" prov:"platform"`
	Digest            string                 `json:"digest" prov:"platform"`
	ExpiresAt         *string                `json:"expiresAt,omitempty" prov:"platform"`
	TargetCount       int                    `json:"targetCount"`
	Preview           []AlarmProposalPreview `json:"preview"`
	Proposal          *Proposal              `json:"proposal,omitempty"`
}

const proposalFields = `token kind state targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note`

var proposeAlarmActionMutation = newDocument("device-management", `mutation ProposeAlarmAction($input: AgentAlarmProposalInput!) {
 proposeAlarmAction(input: $input) { status verdict requiredApprovals reasons digest expiresAt targetCount preview { alarmToken state acknowledged hidesLiveCondition } proposal { `+proposalFields+` } }
}`)
var getProposalQuery = newDocument("device-management", `query GetProposal($token: String!) { agentProposal(token: $token) { `+proposalFields+` } }`)
var listMyProposalsQuery = newDocument("device-management", `query ListMyProposals($criteria: AgentProposalSearchCriteria!) { agentProposals(criteria: $criteria) { results { `+proposalFields+` } pagination { totalRecords } } }`)

func proposalCaller(req *mcp.CallToolRequest, required []string) (string, error) {
	if !carriesActScope(req.Extra, required) {
		return "", fmt.Errorf("%s", markedErrorText(actScopeRefusal))
	}
	token, _, err := callerToken(req)
	return token, err
}
func invalidProposalInput(message string) error { return fmt.Errorf("%s", markedErrorText(message)) }
func (t *Tools) ProposeAlarmAction(ctx context.Context, req *mcp.CallToolRequest, in ProposeAlarmActionInput) (*mcp.CallToolResult, ProposalOutcome, error) {
	var out ProposalOutcome
	token, err := proposalCaller(req, []string{coreauth.ScopeActAlarms})
	if err != nil {
		return nil, out, err
	}
	action := ""
	switch in.Action {
	case "ack":
		action = "ACK"
	case "clear":
		action = "CLEAR"
	default:
		return nil, out, invalidProposalInput("action must be ack or clear")
	}
	n := 0
	for _, r := range in.RequestID {
		if !unicode.IsPrint(r) {
			return nil, out, invalidProposalInput("requestId must contain 1 to 128 printable characters")
		}
		n++
	}
	if n < 1 || n > 128 {
		return nil, out, invalidProposalInput("requestId must contain 1 to 128 printable characters")
	}
	if len(in.AlarmTokens) < 1 || len(in.AlarmTokens) > 100 {
		return nil, out, invalidProposalInput("alarmTokens must contain 1 to 100 tokens")
	}
	for _, tok := range in.AlarmTokens {
		if !alarmTokenPattern.MatchString(tok) {
			return nil, out, invalidProposalInput("alarmTokens must be alarm tokens (letters, digits and . _ : - only, at most 128 characters)")
		}
	}
	if in.Rationale != nil && len(*in.Rationale) > 2048 {
		return nil, out, invalidProposalInput("rationale must be at most 2048 bytes")
	}
	input := map[string]any{"action": action, "alarmTokens": in.AlarmTokens, "requestId": in.RequestID}
	if in.Rationale != nil {
		input["rationale"] = *in.Rationale
	}
	if in.DryRun != nil {
		input["dryRun"] = *in.DryRun
	}
	var resp struct {
		Outcome struct {
			ProposalOutcome
			Stored *proposalResponse `json:"proposal"`
		} `json:"proposeAlarmAction"`
	}
	if err = t.gql.Query(ctx, proposeAlarmActionMutation, token, map[string]any{"input": input}, &resp); err != nil {
		return nil, out, err
	}
	out = resp.Outcome.ProposalOutcome
	if resp.Outcome.Stored != nil {
		p := resp.Outcome.Stored.output()
		out.Proposal = &p
	}
	return provenanceResult(out), out, nil
}

type GetProposalInput struct {
	Token string `json:"token"`
}

func (t *Tools) GetProposal(ctx context.Context, req *mcp.CallToolRequest, in GetProposalInput) (*mcp.CallToolResult, Proposal, error) {
	var out Proposal
	token, err := proposalCaller(req, nil)
	if err != nil {
		return nil, out, err
	}
	var resp struct {
		Proposal proposalResponse `json:"agentProposal"`
	}
	if err = t.gql.Query(ctx, getProposalQuery, token, map[string]any{"token": in.Token}, &resp); err != nil {
		return nil, out, err
	}
	out = resp.Proposal.output()
	return provenanceResult(out), out, nil
}

type ListMyProposalsInput struct {
	PageNumber int    `json:"pageNumber,omitempty"`
	PageSize   int    `json:"pageSize,omitempty"`
	State      string `json:"state,omitempty" jsonschema:"optional proposal state"`
	Kind       string `json:"kind,omitempty" jsonschema:"optional proposal kind"`
}
type ListMyProposalsOutput struct {
	Proposals    []Proposal `json:"proposals"`
	TotalRecords int        `json:"totalRecords"`
}

func (t *Tools) ListMyProposals(ctx context.Context, req *mcp.CallToolRequest, in ListMyProposalsInput) (*mcp.CallToolResult, ListMyProposalsOutput, error) {
	out := ListMyProposalsOutput{Proposals: []Proposal{}}
	token, err := proposalCaller(req, nil)
	if err != nil {
		return nil, out, err
	}
	criteria := map[string]any{"pageNumber": clampPageNumber(in.PageNumber), "pageSize": clampPageSize(in.PageSize)}
	if in.State != "" {
		switch in.State {
		case "PROPOSED", "EXECUTED", "REJECTED", "EXPIRED", "REFUSED", "CANCELLED", "KILLED":
			criteria["state"] = in.State
		default:
			return nil, out, invalidProposalInput("invalid proposal state")
		}
	}
	if in.Kind != "" {
		switch in.Kind {
		case "ALARM_ACK", "ALARM_CLEAR":
			criteria["kind"] = in.Kind
		default:
			return nil, out, invalidProposalInput("invalid proposal kind")
		}
	}
	var resp struct {
		Proposals struct {
			Results    []proposalResponse `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"agentProposals"`
	}
	if err = t.gql.Query(ctx, listMyProposalsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, out, err
	}
	out.TotalRecords = resp.Proposals.Pagination.TotalRecords
	for _, p := range resp.Proposals.Results {
		out.Proposals = append(out.Proposals, p.output())
	}
	return provenanceResult(out), out, nil
}

func proposalInputSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action", "alarmTokens", "requestId"}, "properties": map[string]any{
		"action":      map[string]any{"type": "string", "enum": []string{"ack", "clear"}},
		"alarmTokens": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": map[string]any{"type": "string"}},
		"requestId":   map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Caller supplied idempotency key; forwarded unchanged. Use the same key for retries."},
		"rationale":   map[string]any{"type": "string", "description": "Optional agent rationale, at most 2048 bytes."},
		"dryRun":      map[string]any{"type": "boolean"},
	}}
}
func proposalListSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"pageNumber": map[string]any{"type": "integer"}, "pageSize": map[string]any{"type": "integer"},
		"state": map[string]any{"type": "string", "enum": []string{"PROPOSED", "EXECUTED", "REJECTED", "EXPIRED", "REFUSED", "CANCELLED", "KILLED"}},
		"kind":  map[string]any{"type": "string", "enum": []string{"ALARM_ACK", "ALARM_CLEAR"}},
	}}
}
