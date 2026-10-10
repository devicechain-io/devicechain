// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "testing"

// TestContainsPattern pins that every LIKE metacharacter in filter text — including
// the escape character itself — comes out escaped, so the text matches literally.
func TestContainsPattern(t *testing.T) {
	for in, want := range map[string]string{
		"fleet":  `%fleet%`,
		"50%":    `%50\%%`,
		"a_b":    `%a\_b%`,
		`C:\ops`: `%C:\\ops%`,
		`\%_`:    `%\\\%\_%`,
		"":       `%%`,
	} {
		if got := ContainsPattern(in); got != want {
			t.Errorf("ContainsPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
