// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/httptransport"
)

// ProposalTargetReader reads the published snapshot under the initiating caller's
// bearer. It has no service client, credential minting, cache, or proxy transport.
type ProposalTargetReader struct {
	URL  string
	HTTP *http.Client
}

func NewProposalTargetReader(url string) *ProposalTargetReader {
	return &ProposalTargetReader{URL: url, HTTP: &http.Client{
		Timeout: 10 * time.Second, Transport: httptransport.New(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var _ model.ProposalTargetReader = (*ProposalTargetReader)(nil)

const proposalTargetQuery = `query ProposalTarget($d: String!, $k: String!, $p: String, $e: String) {
  devicesByToken(tokens:[$d]) { token id }
  deviceCommandVocabulary(deviceToken:$d) { constrained commands { commandKey parameterSchema encoder impact } }
  validateCommandEnqueue(deviceToken:$d, commandKey:$k, payload:$p) { allowed code reason }
  commandDispatchSchedule(deviceTokens:[$d], commandKey:$k, expiresAt:$e) {
    holds { deviceToken windowToken dispatchAfter dispatchUntil }
    refusals { deviceToken code reason }
  }
  deviceScopeAnchors(deviceTokens:[$d]) { deviceToken anchors }
}`

// nullableProposalText distinguishes an explicit null from an omitted field.
type nullableProposalText struct {
	Present bool
	Value   *string
}

func (v *nullableProposalText) UnmarshalJSON(raw []byte) error {
	v.Present = true
	return json.Unmarshal(raw, &v.Value)
}

type proposalDefinition struct {
	CommandKey      *string              `json:"commandKey"`
	ParameterSchema nullableProposalText `json:"parameterSchema"`
	Encoder         nullableProposalText `json:"encoder"`
	Impact          *string              `json:"impact"`
}

func proposalDefinitionJSON(v nullableProposalText) (any, error) {
	if !v.Present {
		return nil, errors.New("verify: missing command definition field")
	}
	if v.Value == nil {
		return nil, nil
	}
	d := json.NewDecoder(strings.NewReader(*v.Value))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, fmt.Errorf("verify: invalid command definition JSON: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("verify: trailing command definition JSON")
	}
	return value, nil
}

func proposalFingerprint(d proposalDefinition) (string, error) {
	if d.CommandKey == nil || d.Impact == nil {
		return "", errors.New("verify: missing command definition")
	}
	schema, err := proposalDefinitionJSON(d.ParameterSchema)
	if err != nil {
		return "", err
	}
	encoder, err := proposalDefinitionJSON(d.Encoder)
	if err != nil {
		return "", err
	}
	raw, err := actiongate.Canonical(map[string]any{"commandKey": *d.CommandKey, "parameterSchema": schema, "encoder": encoder, "impact": *d.Impact})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

// ReadProposalTarget makes exactly one request. Missing answers are availability
// errors, never invented unconstrained vocabularies or affirmative verdicts.
func (r *ProposalTargetReader) ReadProposalTarget(ctx context.Context, device, command string, payload []byte, cutoff time.Time) (*model.ProposalTarget, error) {
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.TokenType == auth.TokenTypeService {
		return nil, fmt.Errorf("%w: service tokens cannot read proposal targets", auth.ErrForbidden)
	}
	bearer, ok := auth.BearerFromContext(ctx)
	if !ok || strings.TrimSpace(bearer) == "" {
		return nil, errors.New("verify: proposal target requires the caller's bearer")
	}
	var p any
	if payload != nil {
		p = string(payload)
	}
	body, err := json.Marshal(map[string]any{"query": proposalTargetQuery, "variables": map[string]any{"d": device, "k": command, "p": p, "e": cutoff.UTC().Format(time.RFC3339)}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verify: proposal target call: %w", err)
	}
	defer resp.Body.Close()
	const maxResponse = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verify: proposal target HTTP %d", resp.StatusCode)
	}
	if len(raw) > maxResponse {
		return nil, errors.New("verify: proposal target response too large")
	}
	var env struct {
		Data *struct {
			Devices *[]*struct {
				Token *string
				ID    *string
			} `json:"devicesByToken"`
			Vocabulary json.RawMessage `json:"deviceCommandVocabulary"`
			Validation *struct {
				Allowed *bool
				Code    nullableProposalText
				Reason  nullableProposalText
			} `json:"validateCommandEnqueue"`
			Schedule *struct {
				Holds    *[]*struct{ DeviceToken, WindowToken, DispatchAfter, DispatchUntil *string }
				Refusals *[]*struct{ DeviceToken, Code, Reason *string }
			} `json:"commandDispatchSchedule"`
			Anchors *[]*struct {
				DeviceToken *string
				Anchors     *[]*string
			} `json:"deviceScopeAnchors"`
		}
		Errors []struct{ Extensions struct{ Code string } }
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	for _, e := range env.Errors {
		if e.Extensions.Code == "FORBIDDEN" {
			return nil, fmt.Errorf("%w: proposal target read refused", auth.ErrForbidden)
		}
	}
	if len(env.Errors) != 0 {
		return nil, errors.New("verify: proposal target GraphQL error")
	}
	d := env.Data
	if d == nil || d.Devices == nil || len(d.Vocabulary) == 0 || d.Validation == nil || d.Validation.Allowed == nil || !d.Validation.Code.Present || !d.Validation.Reason.Present || d.Schedule == nil || d.Schedule.Holds == nil || d.Schedule.Refusals == nil || d.Anchors == nil {
		return nil, errors.New("verify: missing proposal target field")
	}
	out := &model.ProposalTarget{Allowed: *d.Validation.Allowed}
	if d.Validation.Code.Value != nil {
		out.RejectCode = *d.Validation.Code.Value
	}
	if d.Validation.Reason.Value != nil {
		out.RejectReason = *d.Validation.Reason.Value
	}
	for _, id := range *d.Devices {
		if id == nil || id.Token == nil || id.ID == nil || *id.Token != device || out.Incarnation != 0 {
			return nil, errors.New("verify: invalid proposal device identity")
		}
		// 63 bits: the incarnation is stored as a signed BIGINT (sql.NullInt64).
		n, err := strconv.ParseUint(*id.ID, 10, 63)
		if err != nil || n == 0 {
			return nil, errors.New("verify: unreadable proposal device incarnation")
		}
		out.Incarnation = n
	}
	var vocab *struct {
		Constrained *bool
		Commands    *[]*proposalDefinition
	}
	if err := json.Unmarshal(d.Vocabulary, &vocab); err != nil {
		return nil, err
	}
	if vocab != nil {
		if vocab.Constrained == nil || vocab.Commands == nil {
			return nil, errors.New("verify: missing proposal vocabulary field")
		}
		out.Constrained = *vocab.Constrained
		for _, entry := range *vocab.Commands {
			if entry == nil {
				return nil, errors.New("verify: null command definition")
			}
			fingerprint, err := proposalFingerprint(*entry)
			if err != nil {
				return nil, err
			}
			if *entry.CommandKey == command {
				if out.Found {
					return nil, errors.New("verify: duplicate command definition")
				}
				out.Found, out.Impact, out.DefinitionFingerprint = true, *entry.Impact, fingerprint
			}
		}
	}
	out.Exists = out.Incarnation != 0 && vocab != nil
	if (out.Incarnation != 0) != (vocab != nil) {
		return nil, errors.New("verify: inconsistent proposal target identity")
	}
	for _, h := range *d.Schedule.Holds {
		if h == nil || h.DeviceToken == nil || h.WindowToken == nil || h.DispatchAfter == nil || h.DispatchUntil == nil || *h.DeviceToken != device || out.Hold != nil {
			return nil, errors.New("verify: missing or inconsistent proposal hold")
		}
		after, err := time.Parse(time.RFC3339, *h.DispatchAfter)
		if err != nil {
			return nil, err
		}
		until, err := time.Parse(time.RFC3339, *h.DispatchUntil)
		if err != nil {
			return nil, err
		}
		out.Hold = &model.DispatchHold{DeviceToken: device, WindowToken: *h.WindowToken, DispatchAfter: after, DispatchUntil: until}
	}
	for _, refusal := range *d.Schedule.Refusals {
		if refusal == nil || refusal.DeviceToken == nil || refusal.Code == nil || refusal.Reason == nil || *refusal.DeviceToken != device || out.ScheduleRefusal != nil {
			return nil, errors.New("verify: missing or inconsistent schedule refusal")
		}
		out.ScheduleRefusal = &model.BatchDeviceRefusal{DeviceToken: device, Code: model.RejectionCode(*refusal.Code), Reason: *refusal.Reason}
	}
	for _, a := range *d.Anchors {
		if a == nil || a.DeviceToken == nil || a.Anchors == nil || *a.DeviceToken != device || out.Anchors != nil {
			return nil, errors.New("verify: missing or inconsistent proposal anchors")
		}
		out.Anchors = []string{}
		for _, anchor := range *a.Anchors {
			if anchor == nil {
				return nil, errors.New("verify: null scope anchor")
			}
			out.Anchors = append(out.Anchors, *anchor)
		}
	}
	if out.Exists && out.Anchors == nil {
		return nil, errors.New("verify: proposal target anchors absent")
	}
	return out, nil
}
