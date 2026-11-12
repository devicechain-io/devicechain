// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/httptransport"
)

// Verdict is a decision's outcome.
type Verdict string

const (
	VerdictRefuse  Verdict = "REFUSE"
	VerdictAuto    Verdict = "AUTO"
	VerdictApprove Verdict = "APPROVE"
)

// ReasonDependencyChanged means the stored alarm cycle no longer matches.
const ReasonDependencyChanged = "DEPENDENCY_CHANGED"

// ReasonAutoNotAvailable is the reason an AUTO verdict is read as a refusal by this release.
const ReasonAutoNotAvailable = "AUTO_NOT_AVAILABLE"

// ReasonProposerNotInStanding is the persisted reason for any proposer standing refusal.
const ReasonProposerNotInStanding = "PROPOSER_NOT_IN_STANDING"

// PersistedReasons removes private proposer details and deduplicates reasons in order.
func PersistedReasons(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	seen := make(map[string]bool)
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "PROPOSER_") {
			reason = ReasonProposerNotInStanding
		}
		if !seen[reason] {
			out = append(out, reason)
			seen[reason] = true
		}
	}
	return out
}

// MaxExpiry is the longest any proposal waits for approval, whatever a decision says.
const MaxExpiry = 24 * time.Hour

// DefaultExpiry is the expiry used when a decision does not state one.
const DefaultExpiry = time.Hour

// ActuationRequest is what a proposal asks the tenant's policy about. The client is never part of
// it: user-management reads it from the caller's own token.
type ActuationRequest struct {
	Kind        Kind
	Impact      string
	TargetCount int
}

// ActuationDecision is the tenant's verdict on a proposal, as agentActuationDecision answers it.
type ActuationDecision struct {
	Verdict           Verdict
	RequiredApprovals int
	Reasons           []string
	PolicyVersion     int
	// Expiry is how long the proposal may wait, already bounded by MaxExpiry.
	Expiry time.Duration
	// ActionsPerHour is the policy's narrowing of the platform rate, 0 for none.
	ActionsPerHour int
	// ClientAttested is whether the client is one whose identity was authenticated (a
	// confidential client or an agent service account) rather than a label any app can claim.
	ClientAttested bool
}

// ApproverSession binds a principal to the sign-in that gave their approval.
type ApproverSession struct {
	Principal string `json:"principal"`
	Sid       string `json:"sid"`
}

// ExecutionRequest asks user-management whether a stored proposal may still go ahead, from its
// own records and with no cache: the original client, the proposer's standing and every counted
// approver's standing, under the current policy.
type ExecutionRequest struct {
	ApproverSessions  []ApproverSession
	ProposalToken     string
	Kind              Kind
	Impact            string
	TargetCount       int
	ProposerPrincipal string
	ProposerClient    string
	ProposerSid       string
	Approvers         []string
}

// ExecutionDecision is the answer.
type ExecutionDecision struct {
	ActuationGeneration int
	Verdict             Verdict
	RequiredApprovals   int
	// ValidApprovers are current approvers whose named sign-ins remain open under the current epoch.
	ValidApprovers   []string
	Reasons          []string
	PolicyVersion    int
	StepUp           auth.StepUp
	AllowSelfConfirm bool
}

// Decider asks the tenant's decisions. Every call is made UNDER THE CALLER'S OWN TOKEN.
type Decider interface {
	Actuation(ctx context.Context, req ActuationRequest) (ActuationDecision, error)
	Execution(ctx context.Context, req ExecutionRequest) (ExecutionDecision, error)
}

// CallerDecider asks user-management's data plane, presenting the CALLER'S OWN bearer token
// (auth.BearerFromContext): an agent's for the actuation decision, the approving person's for the
// execution decision.
//
// 🔴 NO SERVICE TOKEN, AND NO CACHE. A decision made under a service token would be a decision
// the platform made for itself on the caller's say-so, which is the confused deputy this design
// exists to rule out; so a request with no bearer of its own is refused rather than answered. And
// every call reads user-management now: a kill switch takes effect at the next decision, not at a
// cache's expiry.
type CallerDecider struct {
	// URL is user-management's GraphQL endpoint.
	URL  string
	HTTP *http.Client
}

// sharedTransport keeps these calls off the environment's proxy and the process-wide pool.
var sharedTransport = httptransport.New()

// NewCallerDecider builds a CallerDecider for user-management at url.
func NewCallerDecider(url string) *CallerDecider {
	return &CallerDecider{URL: url, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: sharedTransport,
		// 🔴 The caller's bearer must never follow a redirect anywhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// maxDecisionResponse bounds a decision response.
const maxDecisionResponse = 1 << 20

const actuationQuery = `query AgentActuationDecision($kind: AgentActionKind!, $impact: AgentActionImpact, $targetCount: Int!) {
  agentActuationDecision(kind: $kind, impact: $impact, targetCount: $targetCount) {
    verdict requiredApprovals reasons policyVersion expiresInSeconds actionsPerHour clientAttested
  }
}`

const executionQuery = `query AgentExecutionDecision($input: AgentExecutionDecisionInput!) {
  agentExecutionDecision(input: $input) {
    verdict requiredApprovals validApprovers reasons policyVersion actuationGeneration stepUpMaxAgeSeconds acceptRecoveryCodes allowSelfConfirm
  }
}`

// Actuation asks agentActuationDecision.
func (d *CallerDecider) Actuation(ctx context.Context, req ActuationRequest) (ActuationDecision, error) {
	vars := map[string]any{"kind": string(req.Kind), "targetCount": req.TargetCount}
	if req.Impact != "" {
		vars["impact"] = req.Impact
	}
	var out struct {
		D *struct {
			Verdict           string   `json:"verdict"`
			RequiredApprovals int      `json:"requiredApprovals"`
			Reasons           []string `json:"reasons"`
			PolicyVersion     int      `json:"policyVersion"`
			ExpiresInSeconds  int      `json:"expiresInSeconds"`
			ActionsPerHour    int      `json:"actionsPerHour"`
			ClientAttested    bool     `json:"clientAttested"`
		} `json:"agentActuationDecision"`
	}
	if err := d.post(ctx, actuationQuery, vars, &out); err != nil {
		return ActuationDecision{}, err
	}
	if out.D == nil {
		return ActuationDecision{}, errors.New("actiongate: the actuation decision is missing")
	}
	v, err := parseVerdict(out.D.Verdict, out.D.RequiredApprovals)
	if err != nil {
		return ActuationDecision{}, err
	}
	return ActuationDecision{
		Verdict: v, RequiredApprovals: out.D.RequiredApprovals, Reasons: out.D.Reasons,
		PolicyVersion: out.D.PolicyVersion, Expiry: BoundExpiry(time.Duration(out.D.ExpiresInSeconds) * time.Second),
		ActionsPerHour: out.D.ActionsPerHour, ClientAttested: out.D.ClientAttested,
	}, nil
}

// Execution asks agentExecutionDecision.
func (d *CallerDecider) Execution(ctx context.Context, req ExecutionRequest) (ExecutionDecision, error) {
	// 🔴 Always send the session field, even empty: an older server must reject it.
	sessions := req.ApproverSessions
	if sessions == nil {
		sessions = []ApproverSession{}
	}
	input := map[string]any{
		"approverSessions": sessions,
		"proposalToken":    req.ProposalToken, "kind": string(req.Kind),
		"targetCount": req.TargetCount, "proposerPrincipal": req.ProposerPrincipal,
		"proposerClient": req.ProposerClient, "approvers": nonNil(req.Approvers),
	}
	if req.Impact != "" {
		input["impact"] = req.Impact
	}
	if req.ProposerSid != "" {
		input["proposerSid"] = req.ProposerSid
	}
	var out struct {
		D *struct {
			ActuationGeneration *int     `json:"actuationGeneration"`
			Verdict             string   `json:"verdict"`
			RequiredApprovals   int      `json:"requiredApprovals"`
			ValidApprovers      []string `json:"validApprovers"`
			Reasons             []string `json:"reasons"`
			PolicyVersion       int      `json:"policyVersion"`
			StepUpMaxAgeSeconds int      `json:"stepUpMaxAgeSeconds"`
			AcceptRecoveryCodes bool     `json:"acceptRecoveryCodes"`
			AllowSelfConfirm    bool     `json:"allowSelfConfirm"`
		} `json:"agentExecutionDecision"`
	}
	if err := d.post(ctx, executionQuery, map[string]any{"input": input}, &out); err != nil {
		return ExecutionDecision{}, err
	}
	if out.D == nil || out.D.ActuationGeneration == nil || *out.D.ActuationGeneration < 0 {
		return ExecutionDecision{}, errors.New("actiongate: the execution decision is missing")
	}
	v, err := parseVerdict(out.D.Verdict, out.D.RequiredApprovals)
	if err != nil {
		return ExecutionDecision{}, err
	}
	return ExecutionDecision{
		ActuationGeneration: *out.D.ActuationGeneration,
		Verdict:             v, RequiredApprovals: out.D.RequiredApprovals, ValidApprovers: out.D.ValidApprovers,
		Reasons: out.D.Reasons, PolicyVersion: out.D.PolicyVersion,
		StepUp: auth.StepUp{
			MaxAge:              time.Duration(out.D.StepUpMaxAgeSeconds) * time.Second,
			AcceptRecoveryCodes: out.D.AcceptRecoveryCodes,
		},
		AllowSelfConfirm: out.D.AllowSelfConfirm,
	}, nil
}

// BoundExpiry applies the code floor to a decision's expiry: absent or non-positive is
// DefaultExpiry, and nothing waits longer than MaxExpiry.
func BoundExpiry(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultExpiry
	case d > MaxExpiry:
		return MaxExpiry
	}
	return d
}

// parseVerdict reads a verdict fail-closed: an unknown verdict is an error (the caller refuses),
// and an approval verdict must name one or two approvals.
func parseVerdict(s string, required int) (Verdict, error) {
	switch v := Verdict(s); v {
	case VerdictRefuse, VerdictAuto:
		return v, nil
	case VerdictApprove:
		if required < 1 || required > 2 {
			return "", fmt.Errorf("actiongate: an approval verdict requiring %d approvals is not one this release reads", required)
		}
		return v, nil
	}
	return "", fmt.Errorf("actiongate: unknown verdict %q", s)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// post sends one GraphQL request to user-management under the caller's own bearer.
func (d *CallerDecider) post(ctx context.Context, query string, vars map[string]any, out any) error {
	bearer, ok := auth.BearerFromContext(ctx)
	if !ok {
		return errors.New("actiongate: the request carries no token of its own to ask the decision under")
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("actiongate: decision call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDecisionResponse+1))
	if err != nil {
		return fmt.Errorf("actiongate: decision read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("actiongate: decision call answered HTTP %d", resp.StatusCode)
	}
	if len(raw) > maxDecisionResponse {
		return errors.New("actiongate: the decision response is too large")
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("actiongate: decision decode: %w", err)
	}
	if len(env.Errors) > 0 {
		e := env.Errors[0]
		if e.Extensions.Code == "FORBIDDEN" {
			return fmt.Errorf("%w: the decision was refused: %s", auth.ErrForbidden, e.Message)
		}
		return fmt.Errorf("actiongate: the decision answered an error: %s", e.Message)
	}
	return json.Unmarshal(env.Data, out)
}
