// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"net/http"
	"net/url"
)

// SsoCallbackPattern is the route every connection's redirect URI lands on, one path per
// connection, so a callback can be checked against the connection it arrived at.
const SsoCallbackPattern = "GET /sso/callback/{connectionToken}"

// SsoCallbackPrefix is the fixed part of that path.
const SsoCallbackPrefix = "/sso/callback/"

// SsoCallbackHandler serves the provider's redirect. It processes the callback and answers
// with a 302 to the return address (carrying a login code, a link result or a coarse error
// class), or, when the state was unusable and there is no trustworthy place to send the
// browser, a plain error page. It is the only handler in this service reached by a browser
// redirect from outside, so it sets the headers that keep the query out of caches and out of
// the next page's Referer.
func SsoCallbackHandler(m *Manager) http.Handler {
	return BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		res := m.HandleSsoCallback(r.Context(), r.PathValue("connectionToken"), cappedQuery(r.URL.Query()))
		if res.Location == "" {
			http.Error(w, "This sign-in link is not valid or has already been used. Start the sign-in again.", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, res.Location, http.StatusFound)
	}), MaxSmallBodyBytes)
}

// cappedQuery drops any callback parameter longer than the largest legitimate one, so an
// oversized value is never carried into a log line, a store key or a token exchange.
func cappedQuery(q url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"state", "code", "iss", "error"} {
		if v := q.Get(k); len(v) <= 4096 {
			out.Set(k, v)
		}
	}
	return out
}
