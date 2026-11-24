// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"time"
	"unicode"
	"unicode/utf8"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const commandProposalDescription = " Propose sending one command to one device. Nothing is sent unless people approve it in the console; an approved command is then queued and delivered like any other and may still wait for a maintenance window, expire at its cutoff, or fail. Use get_device_capabilities to learn the device's commands. Supply your own requestId unchanged on retries; dryRun checks without storing. Follow your own executed command proposal through get_proposal(area: \"command-delivery\").resultRef (the command token). Command-delivery reads device facts under the caller's own token: the agent and approving person both need device:read (included in MCP's read grant), or the call is refused with FORBIDDEN."

func registerCommandProposals(s *mcp.Server, c *Catalog, t *Tools, enabled bool) {
	registerAction(s, c, &mcp.Tool{Name: "propose_command", Description: proposalNotice + commandProposalDescription, InputSchema: commandProposalInputSchema()}, []string{coreauth.ScopeActCommands}, ActionRisk{Impact: ImpactDestructive, Reversible: false, DestructiveHint: true, Discloses: "stores a request for people to review: the device, command, arguments and the agent's rationale; if people approve it, the device is sent that command"}, enabled, t.ProposeCommand)
}

type ProposeCommandInput struct {
	DeviceToken string  `json:"deviceToken"`
	Name        string  `json:"name"`
	Payload     *string `json:"payload,omitempty"`
	RequestID   string  `json:"requestId"`
	Rationale   *string `json:"rationale,omitempty"`
	ExpiresAt   *string `json:"expiresAt,omitempty"`
	DryRun      *bool   `json:"dryRun,omitempty"`
}

type ProposalOwner struct {
	Area  string `json:"area" prov:"platform"`
	Token string `json:"token" prov:"id"`
}
type CommandProposalText struct {
	DeviceToken *Marked `json:"deviceToken,omitempty" prov:"device-reported"`
	Command     *Marked `json:"command,omitempty" prov:"agent"`
	Payload     *Marked `json:"payload,omitempty" prov:"agent"`
}
type CommandProposalPreview struct {
	DeviceToken    string               `json:"deviceToken,omitempty" prov:"id"`
	Incarnation    string               `json:"incarnation" prov:"platform"`
	PayloadPresent bool                 `json:"payloadPresent"`
	Impact         string               `json:"impact" prov:"platform"`
	Definition     string               `json:"definition" prov:"platform"`
	Cutoff         string               `json:"cutoff" prov:"platform"`
	DispatchAfter  *string              `json:"dispatchAfter,omitempty" prov:"platform"`
	WindowToken    *string              `json:"windowToken,omitempty" prov:"id"`
	Untrusted      *CommandProposalText `json:"untrusted"`
}
type commandPreviewResponse struct {
	DeviceToken    string  `json:"deviceToken"`
	Incarnation    string  `json:"incarnation"`
	Command        string  `json:"command"`
	Payload        *string `json:"payload"`
	PayloadPresent bool    `json:"payloadPresent"`
	Impact         string  `json:"impact"`
	Definition     string  `json:"definition"`
	Cutoff         string  `json:"cutoff"`
	DispatchAfter  *string `json:"dispatchAfter"`
	WindowToken    *string `json:"windowToken"`
}

func (p commandPreviewResponse) output() *CommandProposalPreview {
	out := &CommandProposalPreview{Incarnation: p.Incarnation, PayloadPresent: p.PayloadPresent, Impact: p.Impact, Definition: p.Definition, Cutoff: p.Cutoff, DispatchAfter: p.DispatchAfter, WindowToken: p.WindowToken, Untrusted: &CommandProposalText{Command: mark(SourceAgent, p.Command)}}
	if core.ValidateToken(p.DeviceToken) == nil {
		out.DeviceToken = p.DeviceToken
	} else {
		out.Untrusted.DeviceToken = &Marked{Value: p.DeviceToken, Source: SourceDevice}
	}
	if p.Payload != nil {
		out.Untrusted.Payload = &Marked{Value: *p.Payload, Source: SourceAgent}
	}
	return out
}

type CommandProposalOutcome struct {
	Status            string                  `json:"status" prov:"platform"`
	Verdict           string                  `json:"verdict" prov:"platform"`
	RequiredApprovals int                     `json:"requiredApprovals"`
	Reasons           []string                `json:"reasons" prov:"platform"`
	Digest            string                  `json:"digest" prov:"platform"`
	ExpiresAt         *string                 `json:"expiresAt,omitempty" prov:"platform"`
	TargetCount       int                     `json:"targetCount"`
	Preview           *CommandProposalPreview `json:"preview,omitempty"`
	Proposal          *Proposal               `json:"proposal,omitempty"`
	Owner             *ProposalOwner          `json:"owner,omitempty"`
}

const commandProposalFields = proposalFields + ` payload deviceToken command commandCutoff resultRef`

// commandProposalSummaryFields is what a LIST of command proposals selects: no command payload
// and no rationale. A summary names the proposal; get_proposal returns the full detail.
const commandProposalSummaryFields = `token kind state targets digest impact targetCount proposerPrincipal proposerClient clientAttested policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note deviceToken command commandCutoff resultRef`

var proposeCommandMutation = newDocument("command-delivery", `mutation ProposeCommand($input: AgentCommandProposalInput!) {
 proposeCommand(input: $input) { status verdict requiredApprovals reasons digest expiresAt targetCount preview { deviceToken incarnation command payload payloadPresent impact definition cutoff dispatchAfter windowToken } proposal { `+commandProposalFields+` } }
}`)
var getCommandProposalQuery = newDocument("command-delivery", `query GetCommandProposal($token: String!) { agentProposal(token: $token) { `+commandProposalFields+` } }`)
var listMyCommandProposalsQuery = newDocument("command-delivery", `query ListMyCommandProposals($criteria: AgentProposalSearchCriteria!) { agentProposals(criteria: $criteria) { results { `+commandProposalSummaryFields+` } pagination { totalRecords } } }`)

func (t *Tools) ProposeCommand(ctx context.Context, req *mcp.CallToolRequest, in ProposeCommandInput) (*mcp.CallToolResult, CommandProposalOutcome, error) {
	var out CommandProposalOutcome
	token, err := proposalCaller(req, []string{coreauth.ScopeActCommands})
	if err != nil {
		return nil, out, err
	}
	if core.ValidateToken(in.DeviceToken) != nil {
		return nil, out, invalidProposalInput("deviceToken must be a device token (letters, digits, hyphens and underscores, starting with a letter or digit; at most 128 bytes)")
	}
	if !utf8.ValidString(in.Name) || len(in.Name) < 1 || len(in.Name) > 128 {
		return nil, out, invalidProposalInput("name must be 1 to 128 bytes of valid text")
	}
	if len(in.RequestID) < 1 || len(in.RequestID) > 128 || !utf8.ValidString(in.RequestID) {
		return nil, out, invalidProposalInput("requestId must contain 1 to 128 printable characters")
	}
	for _, r := range in.RequestID {
		if !unicode.IsPrint(r) {
			return nil, out, invalidProposalInput("requestId must contain 1 to 128 printable characters")
		}
	}
	if in.Payload != nil && (len(*in.Payload) > 65536 || !json.Valid([]byte(*in.Payload))) {
		return nil, out, invalidProposalInput("payload must be a JSON document serialized as a string, at most 65536 bytes")
	}
	if in.Rationale != nil && (!utf8.ValidString(*in.Rationale) || len(*in.Rationale) > 2048) {
		return nil, out, invalidProposalInput("rationale must be valid text of at most 2048 bytes")
	}
	if in.ExpiresAt != nil {
		if _, err := time.Parse(time.RFC3339, *in.ExpiresAt); err != nil {
			return nil, out, invalidProposalInput("expiresAt must be RFC3339")
		}
	}
	// Explicit allowlist: no tenant, impact, authority, cause or delivery options can be forwarded.
	input := map[string]any{"deviceToken": in.DeviceToken, "name": in.Name, "requestId": in.RequestID}
	if in.Payload != nil {
		input["payload"] = *in.Payload
	}
	if in.Rationale != nil {
		input["rationale"] = *in.Rationale
	}
	if in.ExpiresAt != nil {
		input["expiresAt"] = *in.ExpiresAt
	}
	if in.DryRun != nil {
		input["dryRun"] = *in.DryRun
	}
	var resp struct {
		Outcome *struct {
			CommandProposalOutcome
			Preview *commandPreviewResponse `json:"preview"`
			Stored  *proposalResponse       `json:"proposal"`
		} `json:"proposeCommand"`
	}
	if err = t.gql.Query(ctx, proposeCommandMutation, token, map[string]any{"input": input}, &resp); err != nil {
		recordProposalError(ctx, err)
		return nil, out, err
	}
	if resp.Outcome == nil || resp.Outcome.Status == "" || resp.Outcome.Verdict == "" {
		return nil, out, invalidProposalInput("command proposal response is unavailable or incomplete")
	}
	// A stored outcome must carry its stored proposal: PENDING_APPROVAL or EXISTING with no
	// proposal is an incomplete answer, never a success the agent can act on.
	if (resp.Outcome.Status == "PENDING_APPROVAL" || resp.Outcome.Status == "EXISTING") && resp.Outcome.Stored == nil {
		return nil, out, invalidProposalInput("command proposal response is unavailable or incomplete")
	}
	out = resp.Outcome.CommandProposalOutcome
	recordProposalDecision(ctx, resp.Outcome.Stored, resp.Outcome.Verdict, resp.Outcome.Reasons)
	if resp.Outcome.Preview != nil {
		out.Preview = resp.Outcome.Preview.output()
	}
	if resp.Outcome.Stored != nil {
		p := resp.Outcome.Stored.commandOutput()
		out.Proposal = &p
		out.Owner = p.Owner
	}
	return provenanceResult(out), out, nil
}

func proposalArea(area string) (bool, error) {
	switch area {
	case "", "device-management":
		return false, nil
	case "command-delivery":
		return true, nil
	default:
		return false, invalidProposalInput("area must be device-management or command-delivery")
	}
}
func proposalReadCaller(req *mcp.CallToolRequest, command bool) (string, error) {
	if command {
		return proposalCaller(req, []string{coreauth.ScopeActCommands})
	}
	return proposalCaller(req, nil)
}
func (p proposalResponse) commandOutput() Proposal {
	out := p.output()
	out.Owner = &ProposalOwner{Area: "command-delivery", Token: p.Token}
	out.ResultRef = p.ResultRef
	out.CommandCutoff = p.CommandCutoff
	out.Untrusted.Command = mark(SourceAgent, p.Command)
	out.Untrusted.Payload = mark(SourceAgent, p.Payload)
	if core.ValidateToken(p.DeviceToken) == nil {
		out.DeviceToken = p.DeviceToken
	} else {
		out.Untrusted.DeviceToken = &Marked{Value: p.DeviceToken, Source: SourceDevice}
	}
	out.Targets = make([]string, 0, len(p.Targets))
	out.Untrusted.OtherTargets = nil
	out.TargetsNote = ""
	for _, target := range p.Targets {
		if core.ValidateToken(target) == nil {
			out.Targets = append(out.Targets, target)
		} else {
			out.Untrusted.OtherTargets = append(out.Untrusted.OtherTargets, &Marked{Value: target, Source: SourceDevice})
		}
	}
	if len(out.Untrusted.OtherTargets) > 0 {
		out.TargetsNote = "some stored targets are not device tokens and are shown as untrusted text"
	}
	return out
}

func proposalGetSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"token"}, "properties": map[string]any{"token": map[string]any{"type": "string"}, "area": proposalAreaSchema()}}
}
func proposalAreaSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"device-management", "command-delivery"}, "description": "Defaults to device-management; command-delivery requires act:commands."}
}
func commandProposalInputSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"deviceToken", "name", "requestId"}, "properties": map[string]any{
		"deviceToken": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$", "minLength": 1, "maxLength": 128},
		"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Published command name, at most 128 bytes."},
		"payload":     map[string]any{"type": "string", "maxLength": 65536, "description": "Optional JSON document serialized as a string, at most 65536 bytes."},
		"requestId":   map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Caller supplied printable idempotency key, at most 128 bytes; forwarded unchanged on retries."},
		"rationale":   map[string]any{"type": "string", "maxLength": 2048, "description": "Optional untrusted agent rationale, at most 2048 bytes."},
		"expiresAt":   map[string]any{"type": "string", "format": "date-time", "description": "RFC3339 command delivery cutoff; defaults to one hour, at most 24 hours. Fixed on retries."},
		"dryRun":      map[string]any{"type": "boolean"},
	}}
}
