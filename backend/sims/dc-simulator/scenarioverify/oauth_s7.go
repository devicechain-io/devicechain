// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

const s7Redirect = "http://127.0.0.1:1/harborpulse-callback"

// S7RegisterClient is an operator-only setup operation. This identity is never
// given to the MCP client: every tool uses the consenting tenant member below.
// Register a public PKCE client per run, so there is no client secret to retain.
func S7RegisterClient(ctx context.Context, hs *sim.Handshake, httpc *http.Client, id string, remove bool) error {
	issuer, err := s7Issuer(hs)
	if err != nil {
		return err
	}
	password := os.Getenv("DC_ADMIN_PASSWORD")
	if password == "" || id == "" {
		return fmt.Errorf("client registration requires DC_ADMIN_PASSWORD and a per-run client id")
	}
	admin := userclient.NewAdminSession(httpc, hs.Endpoints.UserGraphQL, auth.DefaultSuperuserEmail, password)
	doc := `mutation($id:String!,$redirect:String!){createOauthClient(request:{clientId:$id,redirectUris:[$redirect],scopes:["read-only"],confidential:false}){client{clientId}}}`
	vars := map[string]any{"id": id, "redirect": s7Redirect}
	if remove {
		doc = `mutation($id:String!){deleteOauthClient(clientId:$id)}`
		vars = map[string]any{"id": id}
	}
	var out any
	if err := admin.Query(ctx, issuer+"/admin/graphql", doc, vars, &out); err != nil {
		return fmt.Errorf("OAuth client registry operation failed")
	}
	return nil
}

func s7Issuer(hs *sim.Handshake) (string, error) {
	const suffix = "/graphql"
	if !strings.HasSuffix(hs.Endpoints.UserGraphQL, suffix) {
		return "", fmt.Errorf("S7 requires the authorization server's user GraphQL endpoint")
	}
	return strings.TrimSuffix(hs.Endpoints.UserGraphQL, suffix), nil
}

func s7Random() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("OAuth randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

var s7IdentityField = regexp.MustCompile(`<input[^>]*name="identity_token"[^>]*value="([^"]+)"`)

// s7Authorize drives the real server-rendered login and consent POSTs. No
// browser, service account, client_credentials grant or locally signed JWT is used.
// All credentials, codes and the PKCE verifier live only in this call's memory.
func s7Authorize(ctx context.Context, httpc *http.Client, hs *sim.Handshake, clientID, resource string) (string, error) {
	issuer, err := s7Issuer(hs)
	if err != nil {
		return "", err
	}
	verifier, err := s7Random()
	if err != nil {
		return "", err
	}
	state, err := s7Random()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	form := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {s7Redirect},
		"scope": {"read-only"}, "state": {state}, "resource": {resource},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"}, "step": {"login"},
		"email": {hs.SimEmail}, "password": {hs.SimPassword},
	}
	c := *httpc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	post := func(endpoint string, values url.Values) (int, http.Header, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
		if err != nil {
			return 0, nil, nil, fmt.Errorf("OAuth request could not be constructed")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := c.Do(req)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("OAuth request failed")
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			return 0, nil, nil, fmt.Errorf("OAuth response unavailable or oversized")
		}
		return res.StatusCode, res.Header, body, nil
	}
	status, _, body, err := post(issuer+"/oauth/authorize", form)
	if err != nil {
		return "", err
	}
	match := s7IdentityField.FindSubmatch(body)
	if status != http.StatusOK || len(match) != 2 {
		return "", fmt.Errorf("OAuth member login did not return consent (check password, MFA and SSO policy)")
	}
	form.Del("email")
	form.Del("password")
	form.Set("step", "consent")
	form.Set("action", "allow")
	form.Set("tenant", hs.Tenant)
	form.Set("identity_token", html.UnescapeString(string(match[1])))
	status, headers, _, err := post(issuer+"/oauth/authorize", form)
	if err != nil {
		return "", err
	}
	location, parseErr := url.Parse(headers.Get("Location"))
	callback, _ := url.Parse(s7Redirect)
	if status != http.StatusFound || parseErr != nil || location.Scheme != callback.Scheme || location.Host != callback.Host || location.Path != callback.Path || location.Query().Get("state") != state || location.Query().Get("code") == "" || location.Query().Get("error") != "" {
		return "", fmt.Errorf("OAuth consent did not return the registered PKCE callback")
	}
	status, _, body, err = post(issuer+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {s7Redirect},
		"code": {location.Query().Get("code")}, "code_verifier": {verifier}, "resource": {resource},
	})
	if err != nil {
		return "", err
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &tokens) != nil || !strings.EqualFold(tokens.TokenType, "Bearer") {
		return "", fmt.Errorf("OAuth authorization-code exchange failed")
	}
	if err := s7MemberClaims(tokens.AccessToken, hs, clientID, resource); err != nil {
		return "", err
	}
	// Signature verification remains the real MCP resource server's job; this
	// additional check refuses an accidental operator/agent/wrong-tenant credential.
	return tokens.AccessToken, nil
}

func s7MemberClaims(token string, hs *sim.Handshake, clientID, resource string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("OAuth server did not return a user access token")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Tenant   string          `json:"tenant"`
		Username string          `json:"username"`
		ClientID string          `json:"client_id"`
		Scope    string          `json:"scope"`
		Sudo     bool            `json:"sudo"`
		Anchors  json.RawMessage `json:"scp"`
		Audience []string        `json:"aud"`
		Expires  int64           `json:"exp"`
	}
	if err != nil || json.Unmarshal(body, &claims) != nil || claims.Tenant != hs.Tenant || claims.Username != hs.SimEmail || claims.ClientID != clientID || claims.Scope != "read-only" || claims.Sudo || len(claims.Anchors) != 0 || !slices.Contains(claims.Audience, resource) || claims.Expires <= time.Now().Unix() {
		return fmt.Errorf("OAuth token is not the requested unscoped tenant member's read-only resource grant")
	}
	return nil
}
