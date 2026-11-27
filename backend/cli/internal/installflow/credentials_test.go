// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"strings"
	"testing"
)

// RedactURLsInText removes the credential parts of every URL in free text (a tool's
// stderr): userinfo, query and fragment. The rest of the text, and the URL's scheme,
// host and path, are kept, so the line still says where.
func TestRedactURLsInText(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		gone     []string
	}{
		{"error: login at https://user:pw-s3cr3t@login.example/cb?code=c0de-s3cr3t#frag-s3cr3t failed",
			"error: login at https://login.example/cb failed", []string{"pw-s3cr3t", "c0de-s3cr3t", "frag-s3cr3t", "user"}},
		{"see http://example.com/docs.", "see http://example.com/docs.", nil},
		{"token \"https://a:b%40c@h.example/x?t=1\"", "token \"https://h.example/x\"", []string{"b%40c", "t=1"}},
		{"no url here: just words", "no url here: just words", nil},
		{"broken https://user:pw-s3cr3t@h.example/%zz trailing", "broken [redacted] trailing", []string{"pw-s3cr3t"}},
		{"(https://user:pw-s3cr3t@h.example/a?b=c),", "(https://h.example/a),", []string{"pw-s3cr3t", "b=c"}},
		{"two\nlines https://k:v@h.example", "two\nlines https://h.example", []string{"k:v"}},
	} {
		got := RedactURLsInText(tc.in)
		if got != tc.want {
			t.Errorf("RedactURLsInText(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for _, g := range tc.gone {
			if strings.Contains(got, g) {
				t.Errorf("%q still carries %q", got, g)
			}
		}
	}
}
