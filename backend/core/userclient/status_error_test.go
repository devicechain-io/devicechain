// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// statusServer answers every request with status and body.
func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A refused request carries its status as a value, and its text is the text this
// client has always produced — a reader of the message sees no difference.
func TestANon200AnswerIsAStatusErrorWithTheSameText(t *testing.T) {
	srv := statusServer(t, http.StatusUnauthorized, "invalid or expired token\n")
	err := graphqlPost(context.Background(), nil, srv.URL, nil, "query { x }", nil, nil)

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("a 401 was not a *StatusError: %#v", err)
	}
	if se.StatusCode != http.StatusUnauthorized || se.Body != "invalid or expired token" || se.URL != srv.URL {
		t.Fatalf("StatusError = %+v, want 401 / %q / %q", *se, "invalid or expired token", srv.URL)
	}
	if want := "userclient: " + srv.URL + " returned 401: invalid or expired token"; err.Error() != want {
		t.Fatalf("error text changed:\n got %q\nwant %q", err.Error(), want)
	}
}

// Only a 401 is "the endpoint refused the token". A 403 is a token that WAS accepted
// and lacks a permission, and a GraphQL error that mentions authorization is the
// resolver talking — treating either as a refused token would retry a real denial.
func TestIsUnauthorizedIsTrueOnlyForA401(t *testing.T) {
	statusErr := func(code int) error {
		return graphqlPost(context.Background(), nil, statusServer(t, code, "no").URL, nil, "query { x }", nil, nil)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	dialErr := graphqlPost(context.Background(), nil, closed.URL, nil, "query { x }", nil, nil)
	if dialErr == nil {
		t.Fatal("a closed server answered")
	}

	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"401":                  {statusErr(http.StatusUnauthorized), true},
		"401, wrapped":         {fmt.Errorf("read x: %w", statusErr(http.StatusUnauthorized)), true},
		"403":                  {statusErr(http.StatusForbidden), false},
		"500":                  {statusErr(http.StatusInternalServerError), false},
		"502":                  {statusErr(http.StatusBadGateway), false},
		"GraphQL unauthorized": {&GraphQLError{URL: "u", Messages: []string{"unauthorized"}, Codes: []string{"UNAUTHENTICATED"}}, false},
		"transport failure":    {dialErr, false},
		"nil":                  {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsUnauthorized(tc.err); got != tc.want {
				t.Fatalf("IsUnauthorized(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The status must survive the session layer, because that is the caller a
// data-plane client reads through: a type that exists only on graphqlPost's return
// would answer false at every call site that matters.
func TestTenantSessionQuerySurfacesTheStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		switch {
		case strings.Contains(req.Query, "login("):
			_, _ = fmt.Fprintf(w, `{"data":{"login":{"identityToken":"id","expiresAt":%q}}}`, exp)
		case strings.Contains(req.Query, "selectTenant("):
			_, _ = fmt.Fprintf(w, `{"data":{"selectTenant":{"accessToken":"tok","refreshToken":"r","expiresAt":%q}}}`, exp)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := NewTenantSession(nil, srv.URL+"/user", "e@x", "pw", "t")
	err := s.Query(context.Background(), srv.URL+"/data", "query { x }", nil, nil)
	if !IsUnauthorized(err) {
		t.Fatalf("a 401 from the data plane did not reach the session's caller as one: %#v", err)
	}
}
