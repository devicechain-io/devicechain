// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"net/url"
	"strings"
)

// URLCredentials are the credential parts of every URL in s: its userinfo (as
// written and decoded, and the password alone), its query and its fragment, the
// parts a URL carries a token in. A value with no such part gives none. They are
// for registration with a hub, so that a text that echoes the URL is redacted of
// them, whichever surface it reaches.
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
		add(u.RawQuery)
		add(u.Fragment)
	}
	return out
}
