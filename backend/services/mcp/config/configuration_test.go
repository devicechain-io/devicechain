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
