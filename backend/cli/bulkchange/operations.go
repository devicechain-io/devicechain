// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

const changeFields = `token name kind dispatch evidence pauseReason holdCode holdSince executor lastProgressAt lastErrorCode executionEpoch requestDigest planDigest contractVersion contractDigest contract intent targetKind targetCount grantee initiator startedAt dispatchedAt closedAt expiresAt safety { maxPostsPerSecond effectivePostsPerSecond failureThreshold effectiveFailureThreshold ambiguityThreshold effectiveAmbiguityThreshold settleDeadlineSeconds } evidenceCapability { brokerFailClosed reportLog note } progress { planned posted refused skipped cancelled confirmed failed superseded ambiguous resolved awaitingEvidence }`
const statusQuery = `query($token: String!) { change: bulkChange(token: $token) { ` + changeFields + ` } }`
const planQuery = `query($request: BulkChangeCreateRequest!) { plan: planBulkChange(request: $request) { admittedCount refusedCount refusalCounts { code count } refusals { ordinal token code } requestDigest planDigest contractDigest } }`
const createQuery = `mutation($request: BulkChangeCreateRequest!) { change: createBulkChange(request: $request) { ` + changeFields + ` } }`
const startQuery = `mutation($token: String!, $digest: String!) { change: startBulkChange(token: $token, contractDigest: $digest) { ` + changeFields + ` } }`
const pauseQuery = `mutation($token: String!) { change: pauseBulkChange(token: $token) { ` + changeFields + ` } }`
const abortQuery = `mutation($token: String!) { change: abortBulkChange(token: $token) { ` + changeFields + ` } }`
const resumeQuery = `mutation($token: String!, $digest: String!, $safety: BulkChangeSafetyInput) { change: resumeBulkChange(token: $token, contractDigest: $digest, safety: $safety) { ` + changeFields + ` } }`
const resumePreviewQuery = `query($token: String!, $safety: BulkChangeSafetyInput) { preview: previewBulkChangeResume(token: $token, safety: $safety) { withinContract contractVersion contractDigest contract stepUpRequired thresholdStillExceeded } }`
const linesQuery = `query($token: String!, $criteria: BulkChangeLineSearchCriteria!) { lines: bulkChangeLines(token: $token, criteria: $criteria) { results { ordinal targetToken redacted posting settlement code detail prior postedValue postedRevision postedAt settleBy settledAt evidenceBasis evidenceRevision evidenceFlag attempts lastErrorCode overrideChanged resolvedBy resolvedAt } pagination { pageStart pageEnd totalRecords } } }`

type Change struct {
	Token              string         `json:"token"`
	Name               string         `json:"name"`
	Kind               string         `json:"kind"`
	Dispatch           string         `json:"dispatch"`
	Evidence           string         `json:"evidence"`
	ContractDigest     string         `json:"contractDigest"`
	Contract           string         `json:"contract"`
	ContractVersion    int            `json:"contractVersion"`
	RequestDigest      string         `json:"requestDigest"`
	PlanDigest         string         `json:"planDigest"`
	Intent             string         `json:"intent"`
	TargetKind         string         `json:"targetKind"`
	TargetCount        int            `json:"targetCount"`
	ExecutionEpoch     int            `json:"executionEpoch"`
	Executor           string         `json:"executor"`
	PauseReason        *string        `json:"pauseReason"`
	HoldCode           *string        `json:"holdCode"`
	HoldSince          *string        `json:"holdSince"`
	LastProgressAt     *string        `json:"lastProgressAt"`
	LastErrorCode      *string        `json:"lastErrorCode"`
	Grantee            *string        `json:"grantee"`
	Initiator          string         `json:"initiator"`
	StartedAt          *string        `json:"startedAt"`
	DispatchedAt       *string        `json:"dispatchedAt"`
	ClosedAt           *string        `json:"closedAt"`
	ExpiresAt          *string        `json:"expiresAt"`
	Safety             map[string]any `json:"safety"`
	EvidenceCapability map[string]any `json:"evidenceCapability"`
	Progress           map[string]int `json:"progress"`
}

type Plan struct {
	Token         string `json:"token"`
	AdmittedCount int    `json:"admittedCount"`
	RefusedCount  int    `json:"refusedCount"`
	RefusalCounts []struct {
		Code  string `json:"code"`
		Count int    `json:"count"`
	} `json:"refusalCounts"`
	Refusals []struct {
		Ordinal int    `json:"ordinal"`
		Token   string `json:"token"`
		Code    string `json:"code"`
	} `json:"refusals"`
	RequestDigest  string `json:"requestDigest"`
	PlanDigest     string `json:"planDigest"`
	ContractDigest string `json:"contractDigest"`
}

type Line struct {
	Ordinal          int     `json:"ordinal"`
	InputToken       *string `json:"inputToken,omitempty"`
	TargetToken      *string `json:"targetToken"`
	Redacted         bool    `json:"redacted"`
	Posting          string  `json:"posting"`
	Settlement       *string `json:"settlement"`
	Code             *string `json:"code"`
	Detail           *string `json:"detail"`
	Prior            *string `json:"prior"`
	PostedValue      *string `json:"postedValue"`
	PostedRevision   *int    `json:"postedRevision"`
	PostedAt         *string `json:"postedAt"`
	SettleBy         *string `json:"settleBy"`
	SettledAt        *string `json:"settledAt"`
	EvidenceBasis    *string `json:"evidenceBasis"`
	EvidenceRevision *int    `json:"evidenceRevision"`
	EvidenceFlag     *string `json:"evidenceFlag"`
	Attempts         int     `json:"attempts"`
	LastErrorCode    *string `json:"lastErrorCode"`
	OverrideChanged  bool    `json:"overrideChanged"`
	ResolvedBy       *string `json:"resolvedBy"`
	ResolvedAt       *string `json:"resolvedAt"`
}

// JoinDevices adds caller-supplied provenance, separate from the server's target
// identity. Mirror its first-occurrence token normalization for ordinal matching.
// Never reconstruct an erased identifier, including from the original file.
func JoinDevices(lines []Line, devices []string) {
	ordered := []string{}
	seen := map[string]bool{}
	for _, device := range devices {
		device = strings.TrimSpace(device)
		if !seen[device] {
			seen[device] = true
			ordered = append(ordered, device)
		}
	}
	for i := range lines {
		line := &lines[i]
		if !line.Redacted && line.Ordinal > 0 && line.Ordinal <= len(ordered) {
			value := ordered[line.Ordinal-1]
			line.InputToken = &value
		}
	}
}

// API uses the same schema-negotiating tenant client as the other dcctl APIs.
// Mutations are never retried after dispatch, including during rolling upgrades.
type API struct {
	Client       Client
	Endpoint     string
	PollInterval time.Duration
}

func (a API) query(ctx context.Context, document string, vars map[string]any, out any) error {
	err := a.Client.Query(ctx, a.Endpoint, document, vars, out)
	if err == nil || !schemaMismatch(err) {
		return err
	}
	probe, ok := a.Client.(interface {
		ServesBulkChanges(context.Context, string) (bool, error)
	})
	if !ok {
		return errors.New("still upgrading; retry (bulk-change version probe unavailable)")
	}
	served, probeErr := probe.ServesBulkChanges(ctx, a.Endpoint)
	if probeErr != nil {
		return fmt.Errorf("still upgrading; retry (bulk-change version probe failed: %w)", probeErr)
	}
	if !served {
		var unsupported *userclient.UnsupportedFeatureError
		if errors.As(err, &unsupported) {
			return errors.New("this instance does not serve bulk changes")
		}
		// The operation passed discovery before reaching an older pod. An
		// older answer to the fresh probe cannot erase that support evidence.
	}
	return errors.New("still upgrading; retry")
}

func schemaMismatch(err error) bool {
	var unsupported *userclient.UnsupportedFeatureError
	if errors.As(err, &unsupported) {
		return true
	}
	var gql *userclient.GraphQLError
	if !errors.As(err, &gql) {
		return false
	}
	for _, message := range gql.Messages {
		m := strings.ToLower(message)
		if strings.Contains(m, "unknown field") || strings.Contains(m, "cannot query field") ||
			strings.Contains(m, "has no field") || strings.Contains(m, "unknown type") {
			return true
		}
	}
	return false
}

func (a API) Plan(ctx context.Context, request Request) (*Plan, error) {
	var out struct {
		Plan *Plan `json:"plan"`
	}
	if err := a.query(ctx, planQuery, map[string]any{"request": request}, &out); err != nil {
		return nil, err
	}
	if out.Plan == nil {
		return nil, errors.New("bulk change plan response is missing")
	}
	out.Plan.Token = request.Token
	return out.Plan, nil
}

func (a API) change(ctx context.Context, document string, vars map[string]any) (*Change, error) {
	var out struct {
		Change *Change `json:"change"`
	}
	if err := a.query(ctx, document, vars, &out); err != nil {
		return nil, err
	}
	if out.Change == nil {
		return nil, errors.New("bulk change token not found")
	}
	return out.Change, nil
}

func tokenVariables(token string) (map[string]any, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("bulk change token is required")
	}
	return map[string]any{"token": token}, nil
}

func (a API) Create(ctx context.Context, request Request) (*Change, error) {
	return a.change(ctx, createQuery, map[string]any{"request": request})
}

func (a API) Get(ctx context.Context, token string) (*Change, error) {
	vars, err := tokenVariables(token)
	if err != nil {
		return nil, err
	}
	return a.change(ctx, statusQuery, vars)
}

func (a API) Control(ctx context.Context, action, token, digest string, safety map[string]any) (*Change, error) {
	vars, err := tokenVariables(token)
	if err != nil {
		return nil, err
	}
	var document string
	switch action {
	case "start":
		if strings.TrimSpace(digest) == "" {
			return nil, errors.New("--contract-digest is required: present the server digest you reviewed")
		}
		document, vars["digest"] = startQuery, digest
	case "pause":
		document = pauseQuery
	case "abort":
		document = abortQuery
	case "resume":
		if strings.TrimSpace(digest) == "" {
			var out struct {
				Preview *struct {
					ContractDigest         string `json:"contractDigest"`
					WithinContract         bool   `json:"withinContract"`
					StepUpRequired         bool   `json:"stepUpRequired"`
					ThresholdStillExceeded bool   `json:"thresholdStillExceeded"`
				} `json:"preview"`
			}
			vars["safety"] = safety
			if err := a.query(ctx, resumePreviewQuery, vars, &out); err != nil {
				return nil, err
			}
			if out.Preview == nil || out.Preview.ContractDigest == "" {
				return nil, errors.New("bulk change resume preview is missing its contract digest")
			}
			if !out.Preview.WithinContract || out.Preview.StepUpRequired {
				return nil, errors.New("CONTRACT_WIDENED: this release cannot widen a bulk change contract")
			}
			if out.Preview.ThresholdStillExceeded {
				return nil, errors.New("THRESHOLD_STILL_EXCEEDED: bulk change remains paused")
			}
			digest = out.Preview.ContractDigest
		}
		document, vars["digest"], vars["safety"] = resumeQuery, digest, safety
	default:
		return nil, fmt.Errorf("unknown bulk change control %q", action)
	}
	return a.change(ctx, document, vars)
}

type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }
func (e *StatusError) ExitCode() int { return e.Code }

// StatusCode never equates finished dispatch with finished evidence.
func StatusCode(change *Change) int {
	if change.Dispatch == "DISPATCHED" && change.Evidence == "CLOSED" {
		return 0
	}
	switch change.Dispatch {
	case "PAUSED":
		return 3
	case "ABORTED":
		return 4
	default:
		return 1
	}
}

func PrintJSON(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

func PrintChange(w io.Writer, change *Change, asJSON bool) error {
	if asJSON {
		return PrintJSON(w, change)
	}
	_, err := fmt.Fprintf(w, "token: %s\nname: %s\ndispatch: %s\nevidence: %s\nexecutor: %s\ncontractDigest: %s\ncontract: %s\nprogress: %v\n",
		change.Token, change.Name, change.Dispatch, change.Evidence, change.Executor, change.ContractDigest, change.Contract, change.Progress)
	return err
}

func (a API) Status(ctx context.Context, token string, w io.Writer, watch, asJSON bool) error {
	for {
		change, err := a.Get(ctx, token)
		if err != nil {
			return err
		}
		if err := PrintChange(w, change, asJSON); err != nil {
			return err
		}
		code := StatusCode(change)
		if code == 0 {
			return nil
		}
		if !watch || code == 3 || code == 4 {
			return &StatusError{code, fmt.Sprintf("bulk change dispatch=%s evidence=%s", change.Dispatch, change.Evidence)}
		}
		switch change.Dispatch {
		case "DRAFT", "PLANNING", "READY", "RUNNING", "DISPATCHED":
		default:
			return fmt.Errorf("unknown bulk change dispatch %q", change.Dispatch)
		}
		if change.Evidence != "OPEN" && change.Evidence != "CLOSED" {
			return fmt.Errorf("unknown bulk change evidence %q", change.Evidence)
		}
		interval := a.PollInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Lines reads every page. A missing token is checked separately because a
// successful empty page can also represent retained-header/expired-line data.
func (a API) Lines(ctx context.Context, token, posting, settlement string) ([]Line, error) {
	if _, err := a.Get(ctx, token); err != nil {
		return nil, err
	}
	lines := []Line{}
	for page := 1; ; page++ {
		criteria := map[string]any{"pageNumber": page, "pageSize": 1000}
		if posting != "" {
			criteria["posting"] = posting
		}
		if settlement != "" {
			criteria["settlement"] = settlement
		}
		var out struct {
			Lines *struct {
				Results    []Line `json:"results"`
				Pagination struct {
					PageStart    *int `json:"pageStart"`
					PageEnd      *int `json:"pageEnd"`
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"lines"`
		}
		if err := a.query(ctx, linesQuery, map[string]any{"token": token, "criteria": criteria}, &out); err != nil {
			return nil, err
		}
		if out.Lines == nil || out.Lines.Pagination.TotalRecords == nil || out.Lines.Pagination.PageEnd == nil {
			return nil, errors.New("bulk change lines response is missing pagination")
		}
		p := out.Lines.Pagination
		if len(out.Lines.Results) == 0 {
			if len(lines) < *p.TotalRecords {
				return nil, errors.New("bulk change line pagination made no progress")
			}
			return lines, nil
		}
		for _, line := range out.Lines.Results {
			if line.Ordinal <= 0 || len(lines) > 0 && line.Ordinal <= lines[len(lines)-1].Ordinal {
				return nil, errors.New("bulk change line pagination repeated or reordered an ordinal")
			}
			lines = append(lines, line)
		}
		if *p.PageEnd >= *p.TotalRecords {
			return lines, nil
		}
	}
}

func textValue[T any](value *T) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(*value)
}

func PrintLines(w io.Writer, lines []Line, format string) error {
	if format == "json" {
		return PrintJSON(w, lines)
	}
	if format != "csv" && format != "table" {
		return fmt.Errorf("unknown output format %q: expected table, json or csv", format)
	}
	if format == "table" {
		if _, err := fmt.Fprintln(w, "ordinal\ttargetToken\tinputToken\tposting\tsettlement\tcode"); err != nil {
			return err
		}
		for _, line := range lines {
			if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", line.Ordinal, textValue(line.TargetToken), textValue(line.InputToken), line.Posting, textValue(line.Settlement), textValue(line.Code)); err != nil {
				return err
			}
		}
		return nil
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"ordinal", "targetToken", "redacted", "posting", "settlement", "code", "detail", "prior", "postedValue", "postedRevision", "postedAt", "settleBy", "settledAt", "evidenceBasis", "evidenceRevision", "evidenceFlag", "attempts", "lastErrorCode", "overrideChanged", "resolvedBy", "resolvedAt", "inputToken"}); err != nil {
		return err
	}
	for _, line := range lines {
		if err := cw.Write([]string{strconv.Itoa(line.Ordinal), textValue(line.TargetToken), strconv.FormatBool(line.Redacted), line.Posting, textValue(line.Settlement), textValue(line.Code), textValue(line.Detail), textValue(line.Prior), textValue(line.PostedValue), textValue(line.PostedRevision), textValue(line.PostedAt), textValue(line.SettleBy), textValue(line.SettledAt), textValue(line.EvidenceBasis), textValue(line.EvidenceRevision), textValue(line.EvidenceFlag), strconv.Itoa(line.Attempts), textValue(line.LastErrorCode), strconv.FormatBool(line.OverrideChanged), textValue(line.ResolvedBy), textValue(line.ResolvedAt), textValue(line.InputToken)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
