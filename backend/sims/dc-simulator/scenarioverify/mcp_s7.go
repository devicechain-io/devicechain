// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// s7MCP speaks Streamable HTTP to the deployed server. The transport accepts
// both JSON and SSE replies and carries the same user bearer on every request.
type s7MCP struct {
	http     *http.Client
	endpoint string
	token    string
	session  string
	version  string
	id       int
}

func s7Resource(ctx context.Context, httpc *http.Client, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("invalid MCP endpoint")
	}
	u.Path = "/.well-known/oauth-protected-resource" + strings.TrimRight(u.Path, "/")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	c := *httpc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("MCP resource discovery failed")
	}
	defer res.Body.Close()
	var metadata struct {
		Resource string `json:"resource"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&metadata) != nil || metadata.Resource == "" {
		return "", fmt.Errorf("MCP did not serve protected-resource metadata")
	}
	return metadata.Resource, nil
}

func (m *s7MCP) do(req *http.Request) (*http.Response, error) {
	c := *m.http
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c.Do(req)
}

func (m *s7MCP) rpc(ctx context.Context, method string, params any, out any) error {
	m.id++
	message := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if out != nil {
		message["id"] = m.id
	}
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("MCP request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("MCP request construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if m.session != "" {
		req.Header.Set("Mcp-Session-Id", m.session)
	}
	if m.version != "" {
		req.Header.Set("Mcp-Protocol-Version", m.version)
	}
	res, err := m.do(req)
	if err != nil {
		return fmt.Errorf("MCP request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("MCP %s returned HTTP %d", method, res.StatusCode)
	}
	if session := res.Header.Get("Mcp-Session-Id"); session != "" {
		m.session = session
	}
	if out == nil {
		return nil
	}
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	decode := func(b []byte) bool {
		return json.Unmarshal(b, &response) == nil && response.ID == m.id && len(response.Result) > 0 && len(response.Error) == 0
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(res.Body, (8<<20)+1))
		scanner.Buffer(make([]byte, 4096), 8<<20)
		found := false
		for scanner.Scan() {
			if line := scanner.Bytes(); bytes.HasPrefix(line, []byte("data:")) && decode(bytes.TrimSpace(line[5:])) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("MCP SSE response has no successful matching result")
		}
	} else {
		raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
		if err != nil || len(raw) > 8<<20 {
			return fmt.Errorf("MCP response unavailable or oversized")
		}
		if !decode(raw) {
			return fmt.Errorf("MCP response has no successful matching result")
		}
	}
	if json.Unmarshal(response.Result, out) != nil {
		return fmt.Errorf("MCP result decoding failed")
	}
	return nil
}

func (m *s7MCP) initialize(ctx context.Context) error {
	var out struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := m.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "harborpulse-s7", "version": "1"},
	}, &out); err != nil {
		return err
	}
	if out.ProtocolVersion == "" {
		return fmt.Errorf("MCP initialize omitted protocol version")
	}
	m.version = out.ProtocolVersion
	return m.rpc(ctx, "notifications/initialized", map[string]any{}, nil)
}

func (m *s7MCP) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, m.endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Mcp-Session-Id", m.session)
	req.Header.Set("Mcp-Protocol-Version", m.version)
	if res, err := m.do(req); err == nil {
		res.Body.Close()
	}
}

func (m *s7MCP) call(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	var result struct {
		Structured map[string]any `json:"structuredContent"`
		IsError    bool           `json:"isError"`
	}
	if err := m.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &result); err != nil {
		return nil, err
	}
	if result.IsError || result.Structured == nil {
		return nil, fmt.Errorf("MCP %s did not return structured tool data", tool)
	}
	return s7Flatten(result.Structured).(map[string]any), nil
}

// The provenance envelope labels free text as data. Unwrap only values for
// comparison; no returned text is ever executed or interpreted as instructions.
func s7Flatten(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = s7Flatten(x[i])
		}
	case map[string]any:
		if value, ok := x["value"].(string); ok && x["source"] != nil {
			return value
		}
		if untrusted, ok := x["untrusted"].(map[string]any); ok {
			for key, value := range untrusted {
				x[key] = s7Flatten(value)
			}
			delete(x, "untrusted")
		}
		for key, value := range x {
			x[key] = s7Flatten(value)
		}
	}
	return v
}
