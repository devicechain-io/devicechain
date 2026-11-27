// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"net/url"
	"regexp"
	"strings"
)

// URLCredentials are the credential parts of every URL in s: its userinfo (as
// written and decoded, and the password alone), its query and its fragment, the
// parts a URL carries a token in, each as written and as decoded. A value with no
// such part gives none. A display of the URL that holds any of them is not safe.
func URLCredentials(s string) []string {
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v)
		}
	}
	for _, word := range strings.Fields(s) {
		u, err := url.Parse(word)
		if err != nil || (u.Scheme == "" && u.Host == "") {
			continue
		}
		if u.User != nil {
			add(u.User.String())
			if i := strings.Index(word, "@"); i >= 0 {
				if j := strings.Index(word, "://"); j >= 0 && j+3 < i {
					add(word[j+3 : i])
				}
			}
			if p, ok := u.User.Password(); ok {
				add(p)
			}
		}
		// Each in the form it was written and decoded: a percent-encoded token is
		// still the token.
		add(u.RawQuery)
		if q, err := url.QueryUnescape(u.RawQuery); err == nil && q != u.RawQuery {
			add(q)
		}
		add(u.Fragment)
		if f := u.EscapedFragment(); f != u.Fragment {
			add(f)
		}
	}
	return out
}

// urlToken is a whitespace-free run of text: where a URL in free text can be.
var urlToken = regexp.MustCompile(`\S+`)

// RedactURLsInText is s with every URL in it reduced to its scheme, host and path: the
// parts a URL carries a credential in (its userinfo, query and fragment, URLCredentials)
// are removed. A word that looks like a URL ("://") but does not parse is replaced whole
// with "[redacted]", since what it carries cannot be told. Punctuation around a URL
// (quotes, brackets, a trailing comma) is kept. It is for free text dcctl did not write,
// such as a tool's stderr, before any of it is shown.
func RedactURLsInText(s string) string {
	return urlToken.ReplaceAllStringFunc(s, func(word string) string {
		i := strings.Index(word, "://")
		if i < 0 {
			return word
		}
		start := i
		for start > 0 && isSchemeByte(word[start-1]) {
			start--
		}
		end := len(word)
		for end > i+3 && strings.IndexByte(`"'<>()[]{},;`, word[end-1]) >= 0 {
			end--
		}
		prefix, middle, suffix := word[:start], word[start:end], word[end:]
		u, err := url.Parse(middle)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return prefix + "[redacted]" + suffix
		}
		u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
		return prefix + u.String() + suffix
	})
}

func isSchemeByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b == '+' || b == '-' || b == '.'
}
