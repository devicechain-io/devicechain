// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"net/url"
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
