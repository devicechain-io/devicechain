// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AlarmSuppression is live state. The server may redact a hidden window's token
// to the literal "restricted" for scoped callers; preserve it verbatim.
type AlarmSuppression struct {
	WindowToken string `json:"windowToken" prov:"id" jsonschema:"window token or the opaque literal restricted for scoped callers"`
	Until       string `json:"until" prov:"platform"`
}

var alarmSuppressionStatusQuery = newDocument("device-management", `query AlarmSuppressionStatus($tokens: [String!]!) {
 alarmSuppressionStatus(tokens: $tokens) { alarmToken suppression { windowToken until } }
}`)

// enrichAlarmSuppression runs sequentially inside the tool's existing message
// reservation and HTTP-bound context. As in GetDeviceCapabilities, the tool
// reservation covers all downstream work; limits charge MCP messages rather than
// downstream HTTP requests. Each response is byte-bounded
// by Query and each status batch respects the SDL's 200-token limit. Today's
// alarm pages cap at 100, but chunk here so larger returned pages remain safe.
// History (raisedDuringSuppression) cannot be read from this status document.
// Return true on ANY lookup failure, including old-server validation, so absence
// of a suppression field never masquerades as a successful negative lookup.
func (t *Tools) enrichAlarmSuppression(ctx context.Context, token string, alarms []AlarmSummary) bool {
	const batchSize = 200
	matches := map[string]*AlarmSuppression{}
	for start := 0; start < len(alarms); start += batchSize {
		end := min(start+batchSize, len(alarms))
		tokens := make([]string, 0, end-start)
		for _, a := range alarms[start:end] {
			tokens = append(tokens, a.Token)
		}
		var resp struct {
			Status []struct {
				AlarmToken  string            `json:"alarmToken"`
				Suppression *AlarmSuppression `json:"suppression"`
			} `json:"alarmSuppressionStatus"`
		}
		if err := t.gql.Query(ctx, alarmSuppressionStatusQuery, token, map[string]any{"tokens": tokens}, &resp); err != nil {
			return true
		}
		if resp.Status == nil {
			return true
		}
		for _, status := range resp.Status {
			matches[status.AlarmToken] = status.Suppression
		}
	}
	// Commit only after every chunk succeeds: a failed lookup has no partial overlay.
	for i := range alarms {
		alarms[i].Suppression = matches[alarms[i].Token]
	}
	return false
}

type ListSuppressionWindowsInput struct {
	Status      string `json:"status,omitempty" jsonschema:"optional status: SCHEDULED, ACTIVE or ENDED"`
	TargetToken string `json:"targetToken,omitempty" jsonschema:"optional target token to match"`
	PageNumber  int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize    int    `json:"pageSize,omitempty" jsonschema:"windows per page (default 25, max 100)"`
}

type SuppressionTarget struct {
	Type  string  `json:"type" prov:"platform"`
	Token *string `json:"token,omitempty" prov:"id"`
}

type SuppressionWindowUntrusted struct {
	Name        *Marked   `json:"name,omitempty" prov:"operator"`
	Reason      *Marked   `json:"reason,omitempty" prov:"operator"`
	Description *Marked   `json:"description,omitempty" prov:"operator"`
	AlarmKeys   []*Marked `json:"alarmKeys,omitempty" prov:"operator"`
	CreatedBy   *Marked   `json:"createdBy,omitempty" prov:"operator"`
}

// Recurring weekly slots are deliberately omitted; bounds and timezone are
// returned without interpreting operator-authored schedule JSON.
type SuppressionWindowSummary struct {
	Token          string                      `json:"token" prov:"id"`
	Kind           string                      `json:"kind" prov:"platform"`
	Status         string                      `json:"status" prov:"platform"`
	Targets        []SuppressionTarget         `json:"targets"`
	BypassSeverity string                      `json:"bypassSeverity,omitempty" prov:"platform"`
	StartTime      string                      `json:"startTime,omitempty" prov:"platform"`
	EndTime        string                      `json:"endTime,omitempty" prov:"platform"`
	Timezone       string                      `json:"timezone,omitempty" prov:"platform"`
	ValidFrom      string                      `json:"validFrom,omitempty" prov:"platform"`
	ValidUntil     string                      `json:"validUntil,omitempty" prov:"platform"`
	EndedAt        string                      `json:"endedAt,omitempty" prov:"platform"`
	Untrusted      *SuppressionWindowUntrusted `json:"untrusted,omitempty"`
}

type suppressionWindowRaw struct {
	Token          string              `json:"token"`
	Name           string              `json:"name"`
	Reason         string              `json:"reason"`
	Description    string              `json:"description"`
	Kind           string              `json:"kind"`
	Status         string              `json:"status"`
	Targets        []SuppressionTarget `json:"targets"`
	AlarmKeys      []string            `json:"alarmKeys"`
	BypassSeverity string              `json:"bypassSeverity"`
	StartTime      string              `json:"startTime"`
	EndTime        string              `json:"endTime"`
	Timezone       string              `json:"timezone"`
	ValidFrom      string              `json:"validFrom"`
	ValidUntil     string              `json:"validUntil"`
	EndedAt        string              `json:"endedAt"`
	CreatedBy      string              `json:"createdBy"`
}

type ListSuppressionWindowsOutput struct {
	Windows      []SuppressionWindowSummary `json:"windows"`
	TotalRecords int                        `json:"totalRecords"`
}

var listSuppressionWindowsQuery = newDocument("device-management", `query ListSuppressionWindows($criteria: AlarmSuppressionWindowSearchCriteria!) {
 alarmSuppressionWindows(criteria: $criteria) {
  results { token name reason description kind status targets { type token } alarmKeys bypassSeverity startTime endTime timezone validFrom validUntil endedAt createdBy }
  pagination { totalRecords }
 }
}`)

func (t *Tools) ListSuppressionWindows(ctx context.Context, req *mcp.CallToolRequest, in ListSuppressionWindowsInput) (*mcp.CallToolResult, ListSuppressionWindowsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, ListSuppressionWindowsOutput{}, err
	}
	switch in.Status {
	case "", "SCHEDULED", "ACTIVE", "ENDED":
	default:
		return nil, ListSuppressionWindowsOutput{}, fmt.Errorf("status must be SCHEDULED, ACTIVE or ENDED")
	}
	criteria := map[string]any{"pageNumber": clampPageNumber(in.PageNumber), "pageSize": clampPageSize(in.PageSize)}
	putIfSet(criteria, "status", in.Status)
	putIfSet(criteria, "targetToken", in.TargetToken)
	var resp struct {
		Windows struct {
			Results    []suppressionWindowRaw `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarmSuppressionWindows"`
	}
	if err := t.gql.Query(ctx, listSuppressionWindowsQuery, token, map[string]any{"criteria": criteria}, &resp); err != nil {
		if isGraphQLValidationError(err) {
			return nil, ListSuppressionWindowsOutput{}, fmt.Errorf("suppression windows are not available on this server")
		}
		return nil, ListSuppressionWindowsOutput{}, err
	}
	out := ListSuppressionWindowsOutput{Windows: []SuppressionWindowSummary{}, TotalRecords: resp.Windows.Pagination.TotalRecords}
	for _, w := range resp.Windows.Results {
		u := &SuppressionWindowUntrusted{Name: mark(SourceOperator, w.Name), Reason: mark(SourceOperator, w.Reason), Description: mark(SourceOperator, w.Description), CreatedBy: mark(SourceOperator, w.CreatedBy)}
		for _, key := range w.AlarmKeys {
			if m := mark(SourceOperator, key); m != nil {
				u.AlarmKeys = append(u.AlarmKeys, m)
			}
		}
		out.Windows = append(out.Windows, SuppressionWindowSummary{
			Token: w.Token, Kind: w.Kind, Status: w.Status, Targets: w.Targets, BypassSeverity: w.BypassSeverity,
			StartTime: w.StartTime, EndTime: w.EndTime, Timezone: w.Timezone, ValidFrom: w.ValidFrom, ValidUntil: w.ValidUntil,
			EndedAt: w.EndedAt, Untrusted: u,
		})
	}
	return provenanceResult(out), out, nil
}
