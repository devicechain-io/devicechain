// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A GraphQL refusal comes back typed, carrying each error's extensions.code, so a caller
// can tell one refusal from another without matching message text. The message is the
// same text Query always returned.
func TestGraphQLErrorCarriesExtensionCodes(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{
			{"message": "unknown tenant \"ghost\"", "extensions": map[string]any{"code": "UNKNOWN_TENANT"}},
			{"message": "no code here"},
		}})
	}))
	defer target.Close()

	err := clientFor(mint, "shh").Query(context.Background(), target.URL, "ghost", "query{x}", nil, nil)
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("Query returned %T (%v), want a *GraphQLError", err, err)
	}
	if strings.Join(gqlErr.Codes, ",") != "UNKNOWN_TENANT" {
		t.Errorf("Codes = %q, want exactly the one code the response set", gqlErr.Codes)
	}
	want := "svcclient: " + target.URL + ": unknown tenant \"ghost\"; no code here"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
