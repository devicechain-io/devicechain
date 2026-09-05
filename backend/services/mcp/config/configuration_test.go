// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestValidate(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		issuer   string
		wantErr  bool
	}{
		{"both https ok", "https://mcp.example.com", "https://as.example.com/user-management", false},
		{"http localhost ok", "http://localhost:8080", "http://127.0.0.1:8080", false},
		{"missing resource rejected", "", "https://as.example.com", true},
		{"missing issuer rejected", "https://mcp.example.com", "", true},
		{"http non-localhost resource rejected", "http://mcp.example.com", "https://as.example.com", true},
		{"query in resource rejected", "https://mcp.example.com?x=1", "https://as.example.com", true},
		{"userinfo rejected", "https://u@mcp.example.com", "https://as.example.com", true},
		{"relative rejected", "/mcp", "https://as.example.com", true},
		// 🔴 A TRAILING SLASH IS A SECOND SPELLING OF ONE IDENTIFIER, AND EVERY
		// COMPARISON DOWNSTREAM IS EXACT. The metadata LOCATION normalises it away
		// (RFC 9728 §3.1 removes a terminating slash before inserting), but the
		// document's `resource` field and the token `aud` keep it — so a client that
		// found the document at the normalised location rejects it for naming a
		// different resource, and the audience check would refuse the token too.
		// Both fields are covered, because "the resource one is validated" is exactly
		// the shape of gap this pair exists to close.
		{"trailing slash in resource rejected", "https://mcp.example.com/api/mcp/", "https://as.example.com", true},
		{"trailing slash in issuer rejected", "https://mcp.example.com", "https://as.example.com/user-management/", true},
		// The counterweight: the same identifiers WITHOUT the slash still pass, so
		// the rule above is rejecting the slash rather than the path.
		{"the same identifiers without the slash are fine", "https://mcp.example.com/api/mcp", "https://as.example.com/user-management", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &McpConfiguration{ResourceUrl: tc.resource, IssuerUrl: tc.issuer}
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
