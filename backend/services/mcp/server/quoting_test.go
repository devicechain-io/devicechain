// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import "testing"

// TestQuotedStringValueEscapesQuoteAndBackslash pins the escaping of the
// insufficient_scope challenge's resource_metadata parameter: a double quote or a
// backslash in the value is sent as a quoted-pair, so it cannot close the parameter.
func TestQuotedStringValueEscapesQuoteAndBackslash(t *testing.T) {
	cases := map[string]string{
		`https://mcp.example/.well-known/oauth-protected-resource`: `https://mcp.example/.well-known/oauth-protected-resource`,
		`a"b`:      `a\"b`,
		`a\b`:      `a\\b`,
		`x\", y="`: `x\\\", y=\"`,
	}
	for in, want := range cases {
		if got := quotedStringValue(in); got != want {
			t.Errorf("quotedStringValue(%q) = %q, want %q", in, got, want)
		}
	}
}
