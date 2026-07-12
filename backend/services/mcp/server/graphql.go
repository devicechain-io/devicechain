// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// graphqlPort is the shared data-plane port every service serves /graphql on.
const graphqlPort = 8080

// maxGraphQLResponseBytes caps a downstream GraphQL response so a pathological
// query result cannot exhaust memory (defence in depth; tool queries are bounded).
const maxGraphQLResponseBytes = 8 << 20 // 8 MiB

// GraphQLClient calls the per-area DeviceChain GraphQL endpoints on behalf of the
// MCP caller (ADR-047). It forwards the CALLER'S validated access token as the
// bearer credential — never a service token — so every downstream read inherits
// the user's tenant scope, role checks, and audit trail, and the MCP server
// structurally cannot exceed the calling user (the anti-confused-deputy design).
type GraphQLClient struct {
	http    *http.Client
	baseURL func(area string) string
}

// NewGraphQLClient builds a client that reaches services by in-cluster Service DNS
// (http://<area>:8080/graphql) — not through the external ingress.
func NewGraphQLClient() *GraphQLClient {
	return &GraphQLClient{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: func(area string) string { return fmt.Sprintf("http://%s:%d/graphql", area, graphqlPort) },
	}
}

// gqlError is one entry of a GraphQL response's errors array.
type gqlError struct {
	Message string `json:"message"`
}

// Query executes a GraphQL query against the named area's endpoint, forwarding the
// caller's bearer token, and unmarshals the `data` field into out. A transport
// failure, a non-2xx status, or any GraphQL `errors` entry is returned as an error
// (the tool surfaces it to the model as a failed call).
func (c *GraphQLClient) Query(ctx context.Context, area, token, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshaling query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(area), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s graphql: %w", area, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphQLResponseBytes))
	if err != nil {
		return fmt.Errorf("reading %s graphql response: %w", area, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s graphql returned HTTP %d", area, resp.StatusCode)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decoding %s graphql response: %w", area, err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("%s graphql error: %s", area, envelope.Errors[0].Message)
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("unmarshaling %s graphql data: %w", area, err)
		}
	}
	return nil
}
