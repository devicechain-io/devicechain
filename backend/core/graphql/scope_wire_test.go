// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scopeSDL = `
	schema { query: Query }
	type Query {
		gated: String!
		optedIn: String!
		typed: String!
	}
`

// scopeRoot has one resolver behind Authorize, one that opted in with AuthorizeScoped
// and reports the scope it was handed, and one whose typed error chose its own code.
type scopeRoot struct{}

func (*scopeRoot) Gated(ctx context.Context) (string, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return "", err
	}
	return "all of the tenant", nil
}

func (*scopeRoot) OptedIn(ctx context.Context) (string, error) {
	s, err := auth.AuthorizeScoped(ctx, auth.DeviceRead)
	if err != nil {
		return "", err
	}
	tenant, _ := core.TenantFromContext(ctx)
	return fmt.Sprintf("%s:%v:%s", tenant, s.IsScoped(), strings.Join(s.Anchors(), ",")), nil
}

func (*scopeRoot) Typed() (string, error) {
	return "", &rejected{msg: "rejected", cause: auth.ErrScopeUnsupported}
}

type gqlAnswer struct {
	Data   map[string]string `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func postQuery(t *testing.T, url, token, query string) (int, gqlAnswer) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out gqlAnswer
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal(raw, &out), "body=%s", raw)
	}
	return resp.StatusCode, out
}

// End to end through the data plane: a scoped member's token reaches a resolver behind
// Authorize as FORBIDDEN with the reason SCOPE_UNSUPPORTED, and reaches an opted-in
// resolver with its scope — and its tenant — intact.
func TestAScopedTokenOnTheDataPlane(t *testing.T) {
	iss, v := testIssuerValidator(t)
	gate := core.NewReadinessGate()
	gate.MarkReady(v)
	srv := httptest.NewServer(NewHttpHandler(MustParseSchema(scopeSDL, &scopeRoot{}), map[ContextKey]interface{}{}, gate))
	defer srv.Close()

	scope, err := auth.NewScope([]string{"customer:acme-corp"})
	require.NoError(t, err)
	scoped, err := iss.IssueScopedTenantAccess("acme", "c@acme-corp.example", nil,
		[]string{string(auth.AuthorityAll)}, scope, "j-scoped")
	require.NoError(t, err)

	status, out := postQuery(t, srv.URL, scoped.Token, `{ gated }`)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, out.Errors, 1)
	assert.Equal(t, "FORBIDDEN", out.Errors[0].Extensions["code"])
	assert.Equal(t, "SCOPE_UNSUPPORTED", out.Errors[0].Extensions["reason"])

	status, out = postQuery(t, srv.URL, scoped.Token, `{ optedIn }`)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, out.Errors)
	assert.Equal(t, "acme:true:customer:acme-corp", out.Data["optedIn"])

	// A typed error that chose its own code keeps it, and gains no reason it did not set.
	_, out = postQuery(t, srv.URL, scoped.Token, `{ typed }`)
	require.Len(t, out.Errors, 1)
	assert.Equal(t, "REJECTED", out.Errors[0].Extensions["code"])
	assert.NotContains(t, out.Errors[0].Extensions, "reason")

	// Negative control: the same authorities unscoped pass the gate, and an ordinary
	// refusal carries FORBIDDEN with NO reason.
	plain, err := iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, false, "j-plain")
	require.NoError(t, err)
	_, out = postQuery(t, srv.URL, plain.Token, `{ gated }`)
	require.Empty(t, out.Errors)
	assert.Equal(t, "all of the tenant", out.Data["gated"])

	none, err := iss.IssueTenantAccess("acme", "a@acme.example", nil, nil, false, "j-none")
	require.NoError(t, err)
	_, out = postQuery(t, srv.URL, none.Token, `{ gated }`)
	require.Len(t, out.Errors, 1)
	assert.Equal(t, "FORBIDDEN", out.Errors[0].Extensions["code"])
	assert.NotContains(t, out.Errors[0].Extensions, "reason")
}

// 🔴 role:write is dual-tier since ADR-081, so a tenant access token holding it — every
// tenant-admin's "*" — now SATISFIES it at the check. The operator's role catalogue is
// safe only because it is served on the admin plane, whose handler refuses an access
// token before any resolver runs. This pins that the plane, not the tier, is the guard.
func TestAnAccessTokenHoldingRoleWriteCannotReachTheAdminPlane(t *testing.T) {
	iss, v := testIssuerValidator(t)
	gate := core.NewReadinessGate()
	gate.MarkReady(v)
	srv := httptest.NewServer(NewAdminHttpHandler(MustParseSchema(scopeSDL, &scopeRoot{}), map[ContextKey]interface{}{}, gate))
	defer srv.Close()

	access, err := iss.IssueTenantAccess("acme", "admin@acme.example", nil, []string{string(auth.RoleWrite)}, false, "j-a")
	require.NoError(t, err)
	require.NoError(t, auth.Authorize(auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: []string{string(auth.RoleWrite)},
	}), auth.RoleWrite), "precondition: a tenant access token satisfies role:write at the check")

	status, _ := postQuery(t, srv.URL, access.Token, `{ typed }`)
	assert.Equal(t, http.StatusUnauthorized, status, "the admin plane admitted a tenant access token")

	// Negative control: an identity token is admitted to the same handler.
	id, err := iss.IssueIdentity("op@instance.example", "epoch", "sid", nil, []string{string(auth.RoleWrite)}, "j-i")
	require.NoError(t, err)
	status, _ = postQuery(t, srv.URL, id.Token, `{ typed }`)
	assert.Equal(t, http.StatusOK, status)
}
