// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package svcclient is the synchronous cross-service call primitive (ADR-044
// amendment). It lets one service call another's existing GraphQL endpoint when
// it must validate an invariant it does not own at the moment of an action —
// e.g. "does this device exist right now, before I enqueue a command?" — the case
// an eventually-consistent async projection cannot answer honestly.
//
// Identity reuses the platform JWT machinery whole: the client mints a short-lived
// service token from user-management (presenting the shared service secret), caches
// it until shortly before expiry, and attaches it as a bearer plus an explicit
// tenant header on each call. Because user-management signs the token with its
// normal key, the target service's existing JWKS validator accepts it with no new
// trust configuration; the callee gates the call on the token's authorities.
//
// Use this only for read-time invariant checks. A fact that has already happened
// and flows outward from its owner (an entity deletion) belongs on the async event
// path, not here (the ADR-044 decision rule: propagate facts async, validate
// invariants sync).
package svcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// requestTimeout bounds a single mint or query round-trip.
	requestTimeout = 10 * time.Second
	// refreshSkew re-mints this far ahead of the cached token's expiry so a call
	// never rides a token that expires mid-flight.
	refreshSkew = 1 * time.Minute
	// maxResponseBytes caps a response read so a misbehaving peer cannot exhaust
	// memory.
	maxResponseBytes = 1 << 20
)

// Client makes authenticated GraphQL calls to other services on behalf of one
// calling service identity (subject + authorities). It is safe for concurrent use
// and caches the minted service token across calls. Construct one per calling
// service and share it.
type Client struct {
	http        *http.Client
	mintURL     string
	secret      string
	subject     string
	authorities []string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// New builds a Client that mints tokens carrying subject (the calling service's
// name, for audit) and authorities (the least-privilege capabilities its calls
// need). umCfg locates user-management's mint endpoint; secret is the shared
// service secret (config.ServiceAuthConfiguration.Secret). An empty secret yields
// a Client that fails closed on first use.
func New(umCfg config.UserManagementConfiguration, secret, subject string, authorities []string) *Client {
	return &Client{
		http:        &http.Client{Timeout: requestTimeout},
		mintURL:     fmt.Sprintf("http://%s:%d%s", umCfg.Hostname, umCfg.Port, auth.ServiceTokenPath),
		secret:      secret,
		subject:     subject,
		authorities: authorities,
	}
}

// Query executes a GraphQL operation against baseURL (a target service's GraphQL
// endpoint) in the given tenant and decodes the response "data" object into out.
// It obtains a service token (from cache or a fresh mint), sets it as the bearer,
// and passes the tenant in the ServiceTenantHeader. A non-empty GraphQL "errors"
// array is returned as an error.
func (c *Client) Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error {
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("svcclient: tenant is required")
	}
	token, err := c.serviceToken(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("svcclient: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("svcclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(auth.ServiceTenantHeader, tenant)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("svcclient: call %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("svcclient: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("svcclient: %s returned %d: %s", baseURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("svcclient: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("svcclient: %s: %s", baseURL, strings.Join(msgs, "; "))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("svcclient: decode data: %w", err)
		}
	}
	return nil
}

// serviceToken returns a valid cached token or mints a fresh one. Minting happens
// under the lock so a burst of first calls collapses to a single mint rather than
// stampeding user-management.
func (c *Client) serviceToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiresAt.Add(-refreshSkew)) {
		return c.token, nil
	}
	if c.secret == "" {
		return "", fmt.Errorf("svcclient: service-to-service auth is not configured (empty service secret)")
	}

	body, err := json.Marshal(auth.ServiceTokenRequest{Subject: c.subject, Authorities: c.authorities})
	if err != nil {
		return "", fmt.Errorf("svcclient: marshal mint request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mintURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("svcclient: build mint request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.ServiceSecretHeader, c.secret)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("svcclient: mint token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("svcclient: read mint response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("svcclient: mint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var minted auth.ServiceTokenResponse
	if err := json.Unmarshal(raw, &minted); err != nil {
		return "", fmt.Errorf("svcclient: decode mint response: %w", err)
	}
	if minted.Token == "" {
		return "", fmt.Errorf("svcclient: mint returned an empty token")
	}
	c.token = minted.Token
	c.expiresAt = time.Unix(minted.ExpiresAt, 0)
	return c.token, nil
}
