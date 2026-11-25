// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

type proposalAuditKey struct{}

// This sink belongs to one tools/call, never a session or token. Only the two
// proposal handlers fill it, from the area's decoded response, before returning.
// No tool output, metadata, arguments or untrusted text is scraped for auditing.
type proposalAuditDecision struct {
	proposal, verdict string
}

func recordProposalDecision(ctx context.Context, stored *proposalResponse, verdict string, reasons []string) {
	decision, _ := ctx.Value(proposalAuditKey{}).(*proposalAuditDecision)
	if decision == nil {
		return
	}
	if stored != nil && core.ValidateToken(stored.Token) == nil {
		decision.proposal = stored.Token
	}
	switch verdict {
	case "APPROVE", "REFUSE":
		decision.verdict = verdict
	default:
		return
	}
	if verdict == "REFUSE" {
		if codes := proposalAuditCodes(reasons); len(codes) > 0 {
			decision.verdict += ":" + strings.Join(codes, ",")
		}
	}
}

func recordProposalError(ctx context.Context, err error) {
	var response *graphqlResponseError
	if !errors.As(err, &response) {
		return // Transport failures and free-text errors are not platform verdicts.
	}
	var codes []string
	for _, e := range response.errors {
		if code, ok := e.Extensions["code"].(string); ok {
			codes = append(codes, code)
		}
	}
	if codes = proposalAuditCodes(codes); len(codes) > 0 {
		recordProposalDecision(ctx, nil, "REFUSE", codes)
	}
}

// A grammar such as [A-Z_]+ would still admit secrets and invented codes. This is
// the closed vocabulary returned by proposal admission, policy, counters and target
// validation. Unknown codes are omitted, including codes from a newer peer. Never
// copy messages, rationale, command payloads or caller requestId into either field.
func proposalAuditCode(code string) bool {
	switch code {
	case "INSTANCE_ACTUATION_OFF", "OPERATOR_SUSPENDED", "NO_PUBLISHED_POLICY",
		"TENANT_ACTUATION_OFF", "CLIENT_NOT_ALLOWED", "UNKNOWN_KIND", "UNKNOWN_IMPACT",
		"BAD_TARGET_COUNT", "KIND_REFUSED_BY_POLICY", "COMMAND_NOT_ALLOWED", "GROUP_NOT_ALLOWED",
		"OVER_PER_ACTION_CAP", "OVER_ROLLOUT_GROUP_SIZE", "FLOOR_NEVER_AUTO", "FLOOR_TWO_PERSON",
		"POLICY_TWO_PERSON", "PUBLIC_CLIENT_NOT_AUTO", "TOO_FEW_ELIGIBLE_APPROVERS",
		"NOT_AN_AGENT_PRINCIPAL", "AUTO_NOT_AVAILABLE", "PROPOSER_NOT_IN_STANDING",
		"ALARM_NOT_FOUND", "CLIENT_RATE_EXCEEDED", "PRINCIPAL_RATE_EXCEEDED", "TOO_MANY_OPEN_PROPOSALS",
		"TENANT_OPEN_PROPOSALS_EXCEEDED", "TENANT_RATE_EXCEEDED", "DRY_RUN_RATE_EXCEEDED",
		"DEVICE_NOT_FOUND", "DEVICE_DISABLED", "OPEN_VOCABULARY_NOT_SUPPORTED", "COMMAND_NOT_IN_VOCABULARY",
		"PAYLOAD_REJECTED", "PAYLOAD_NOT_JSON", "PAYLOAD_TOO_LARGE", "PAYLOAD_SCHEMA_VIOLATION",
		"COMMAND_DESTRUCTIVE_REFUSED", "SCHEDULE_REFUSED", "WINDOW_AFTER_EXPIRY", "WINDOW_TOO_LATE",
		"IMPACT_UNKNOWN", "GROUP_RESTRICTION_NOT_SUPPORTED", "DESTRUCTIVE_NEEDS_TWO_APPROVERS", "HUMAN_APPROVAL_REQUIRED",
		"AGENT_ONLY", "SERVICE_TOKEN_REFUSED", "SCOPED_GRANT_NOT_SUPPORTED", "FORBIDDEN",
		"POLICY_UNAVAILABLE", "VALIDATION_UNAVAILABLE", "COMMAND_PROPOSALS_DISABLED", "CONFLICT":
		return true
	default:
		return false
	}
}

func proposalAuditCodes(reasons []string) []string {
	var codes []string
	seen := map[string]bool{}
	for _, code := range reasons {
		if proposalAuditCode(code) && !seen[code] {
			codes = append(codes, code)
			seen[code] = true
		}
	}
	return codes
}
