// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultPageSize / maxPageSize bound a tool's page so a single call can't pull an
// unbounded result set into the model's context (a token-cost + safety concern).
const (
	defaultPageSize = 25
	maxPageSize     = 100
)

// Tools holds the dependencies shared by every read tool.
type Tools struct {
	gql *GraphQLClient
}

// NewTools builds the tool set over a GraphQL client.
func NewTools(gql *GraphQLClient) *Tools { return &Tools{gql: gql} }

// callerToken extracts the caller's forwarded access token (and tenant) from the
// verified TokenInfo the RS middleware attached. Absent it, the call is
// unauthenticated — fail closed.
func callerToken(req *mcp.CallToolRequest) (token, tenant string, err error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.Extra == nil {
		return "", "", fmt.Errorf("unauthenticated: no verified token on the request")
	}
	token, _ = req.Extra.TokenInfo.Extra[extraTokenKey].(string)
	tenant, _ = req.Extra.TokenInfo.Extra[extraTenantKey].(string)
	if token == "" {
		return "", "", fmt.Errorf("unauthenticated: no caller token")
	}
	return token, tenant, nil
}

// clampPageSize bounds a requested page size to [1, maxPageSize], defaulting 0.
func clampPageSize(n int) int {
	if n <= 0 {
		return defaultPageSize
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

func clampPageNumber(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// ---- list_devices ----

// ListDevicesInput is the list_devices tool input.
type ListDevicesInput struct {
	PageNumber int    `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	PageSize   int    `json:"pageSize,omitempty" jsonschema:"devices per page (default 25, max 100)"`
	DeviceType string `json:"deviceType,omitempty" jsonschema:"optional device-type token to filter by"`
}

// DeviceSummary is one device in the list_devices output.
type DeviceSummary struct {
	Token       string `json:"token"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	ExternalId  string `json:"externalId,omitempty"`
	DeviceType  string `json:"deviceType,omitempty"`
}

// ListDevicesOutput is the list_devices tool output.
type ListDevicesOutput struct {
	Devices      []DeviceSummary `json:"devices"`
	TotalRecords int             `json:"totalRecords"`
}

const listDevicesQuery = `query ListDevices($criteria: DeviceSearchCriteria!) {
  devices(criteria: $criteria) {
    results { token name description externalId deviceType { token } }
    pagination { totalRecords }
  }
}`

// ListDevices lists the devices in the caller's tenant (paged), forwarding the
// caller's token so the result is exactly what that user may see.
func (t *Tools) ListDevices(ctx context.Context, req *mcp.CallToolRequest, in ListDevicesInput) (*mcp.CallToolResult, ListDevicesOutput, error) {
	token, _, err := callerToken(req)
	if err != nil {
		return nil, ListDevicesOutput{}, err
	}
	criteria := map[string]any{
		"pageNumber": clampPageNumber(in.PageNumber),
		"pageSize":   clampPageSize(in.PageSize),
	}
	if in.DeviceType != "" {
		criteria["deviceType"] = in.DeviceType
	}

	var resp struct {
		Devices struct {
			Results []struct {
				Token       string `json:"token"`
				Name        string `json:"name"`
				Description string `json:"description"`
				ExternalId  string `json:"externalId"`
				DeviceType  struct {
					Token string `json:"token"`
				} `json:"deviceType"`
			} `json:"results"`
			Pagination struct {
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"devices"`
	}
	if err := t.gql.Query(ctx, "device-management", token, listDevicesQuery, map[string]any{"criteria": criteria}, &resp); err != nil {
		return nil, ListDevicesOutput{}, err
	}

	out := ListDevicesOutput{TotalRecords: resp.Devices.Pagination.TotalRecords}
	for _, d := range resp.Devices.Results {
		out.Devices = append(out.Devices, DeviceSummary{
			Token: d.Token, Name: d.Name, Description: d.Description,
			ExternalId: d.ExternalId, DeviceType: d.DeviceType.Token,
		})
	}
	return nil, out, nil
}
