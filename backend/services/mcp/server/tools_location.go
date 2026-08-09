// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- query_locations ----
//
// The tool that answers "where is device X". It reads event-management's
// locationEvents query, which returns newest-first, so the first result is the
// device's last known position and a bounded window gives its track.
//
// 🔴 AUTHORIZATION IS NOT RE-IMPLEMENTED HERE, AND MUST NOT BE. Like every other
// tool this one forwards the CALLER'S OWN access token (callerToken → gql.Query)
// and the MCP server holds no credential of its own, so the locationEvents resolver
// runs under the caller's claims and applies its own gate. Position is gated on
// `location:read` — an authority deliberately absent from the read-only viewer
// baseline and distinct from the `event:read` that the measurement tools need — so
// an agent acting for a user who was never granted it gets the resolver's refusal
// surfaced as a failed tool call, without this file knowing the authority's name.
// That is the whole point: adding a check here would be a second copy of the policy,
// free to drift from the one that actually protects the data.

type QueryLocationsInput struct {
	DeviceToken string `json:"deviceToken" jsonschema:"the device whose position history to query"`
	StartTime   string `json:"startTime,omitempty" jsonschema:"inclusive RFC3339 start time (optional)"`
	EndTime     string `json:"endTime,omitempty" jsonschema:"exclusive RFC3339 end time (optional)"`
	PageNumber  int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize    int    `json:"pageSize,omitempty" jsonschema:"positions per page (default 25, max 100). Use 1 to ask only where the device is now"`
}

// LocationEvent is one reported position. Every numeric field is a POINTER because a
// receiver reports what it knows: a fix with no speed or heading is normal, and a
// zero there would claim a stationary device pointing due north rather than a device
// that said nothing. Omitted-when-absent keeps that distinction on the wire.
type LocationEvent struct {
	DeviceToken  string   `json:"deviceToken"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Elevation    *float64 `json:"elevation,omitempty"`
	Accuracy     *float64 `json:"accuracy,omitempty"`
	Speed        *float64 `json:"speed,omitempty"`
	Heading      *float64 `json:"heading,omitempty"`
	OccurredTime string   `json:"occurredTime,omitempty"`
}

type QueryLocationsOutput struct {
	Locations    []LocationEvent `json:"locations"`
	TotalRecords int             `json:"totalRecords"`
}

const queryLocationsQuery = `query QueryLocations($criteria: EventSearchCriteria!) {
  locationEvents(criteria: $criteria) {
    results { deviceToken latitude longitude elevation accuracy speed heading occurredTime }
    pagination { totalRecords }
  }
}`

// QueryLocations returns a device's reported positions, newest first (paged,
// bounded), forwarding the caller's token so the result is exactly what that user
// may see — and nothing when they hold no position authority.
func (t *Tools) QueryLocations(ctx context.Context, req *mcp.CallToolRequest, in QueryLocationsInput) (*mcp.CallToolResult, QueryLocationsOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, QueryLocationsOutput{}, err
	}
	if in.DeviceToken == "" {
		return nil, QueryLocationsOutput{}, fmt.Errorf("deviceToken is required")
	}
	criteria := map[string]any{
		"deviceToken": in.DeviceToken,
		"pageNumber":  clampPageNumber(in.PageNumber),
		"pageSize":    clampPageSize(in.PageSize),
	}
	putIfSet(criteria, "startTime", in.StartTime)
	putIfSet(criteria, "endTime", in.EndTime)

	var resp struct {
		LocationEvents struct {
			Results    []LocationEvent `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"locationEvents"`
	}
	if err := t.gql.Query(ctx, "event-management", token, queryLocationsQuery,
		map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, QueryLocationsOutput{}, err
	}
	return nil, QueryLocationsOutput{
		Locations:    resp.LocationEvents.Results,
		TotalRecords: resp.LocationEvents.Pagination.TotalRecords,
	}, nil
}
