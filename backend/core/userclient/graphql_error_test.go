// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// errorServer answers every request with the given GraphQL errors array.
func errorServer(t *testing.T, errorsJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"errors":%s}`, errorsJSON)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url string) error {
	t.Helper()
	err := graphqlPost(context.Background(), nil, url, nil, "mutation { x }", nil, nil)
	if err == nil {
		t.Fatal("a response carrying errors was answered as success")
	}
	return err
}

// The server's extensions.code reaches the caller, and the error text is unchanged.
func TestGraphQLErrorsCarryTheirCodes(t *testing.T) {
	srv := errorServer(t, `[{"message":"m","extensions":{"code":"CONFLICT"}}]`)
	err := post(t, srv.URL)

	if !AllHaveCode(fmt.Errorf("wrapped: %w", err), "CONFLICT") {
		t.Fatalf("CONFLICT was not read off the response: %#v", err)
	}
	if AllHaveCode(err, "THROTTLED") {
		t.Fatal("a CONFLICT response reported THROTTLED")
	}
	if want := "userclient: " + srv.URL + ": m"; err.Error() != want {
		t.Fatalf("error text changed:\n got %q\nwant %q", err.Error(), want)
	}
}

// EVERY error must carry the code: one CONFLICT beside an uncoded failure is not a
// benign refusal, and neither is a response with no code at all.
func TestAllHaveCodeRequiresEveryError(t *testing.T) {
	for name, body := range map[string]string{
		"one coded, one not": `[{"message":"a","extensions":{"code":"CONFLICT"}},{"message":"b"}]`,
		"uncoded first":      `[{"message":"b"},{"message":"a","extensions":{"code":"CONFLICT"}}]`,
		"no code at all":     `[{"message":"tenant already exists"}]`,
		"another code":       `[{"message":"a","extensions":{"code":"THROTTLED"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if AllHaveCode(post(t, errorServer(t, body).URL), "CONFLICT") {
				t.Fatalf("%s was answered as all-CONFLICT", body)
			}
		})
	}
	t.Run("two coded", func(t *testing.T) {
		body := `[{"message":"a","extensions":{"code":"CONFLICT"}},{"message":"b","extensions":{"code":"CONFLICT"}}]`
		if !AllHaveCode(post(t, errorServer(t, body).URL), "CONFLICT") {
			t.Fatal("two CONFLICT errors were not answered as all-CONFLICT")
		}
	})
	t.Run("not a GraphQL error", func(t *testing.T) {
		if AllHaveCode(fmt.Errorf("dial: connection refused"), "CONFLICT") || AllHaveCode(nil, "CONFLICT") {
			t.Fatal("a transport failure was answered as all-CONFLICT")
		}
	})
}
