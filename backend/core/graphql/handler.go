// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	graphql "github.com/graph-gophers/graphql-go"

	"github.com/graph-gophers/graphql-go/relay"
)

// bearerPrefix is the Authorization scheme carrying the access token.
const bearerPrefix = "Bearer "

type ContextKey string

const (
	ContextRdbKey ContextKey = "rdb"
	ContextApiKey ContextKey = "api"
)

// Adds extra context to http request.
type HttpHandler struct {
	Schema           *graphql.Schema
	Relay            *relay.Handler
	ContextProviders map[ContextKey]interface{}
	// Validator verifies the request's access token. When nil the handler runs
	// without JWT authentication (no tenant is derived from a token); requests
	// then have no tenant and fail closed at the DB scope. Production services
	// always supply one.
	Validator *auth.Validator
}

// Create new http handler.
func NewHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, validator *auth.Validator) *HttpHandler {
	handler := &HttpHandler{
		Schema:           schema,
		Relay:            &relay.Handler{Schema: schema},
		ContextProviders: providers,
		Validator:        validator,
	}
	return handler
}

// Handles http request processing.
func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for key, value := range h.ContextProviders {
		r = r.WithContext(context.WithValue(r.Context(), key, value))
	}

	// Authenticate via the verified JWT tenant claim (ADR-008), which replaces
	// the former trusted X-DC-Tenant gateway header. A *present* token is always
	// verified: an invalid one is rejected with 401 rather than silently
	// ignored. An *absent* token is allowed through with no tenant so that
	// unauthenticated entry points (e.g. the user-management login mutation) can
	// run; every tenant-scoped operation still fails closed at the DB layer when
	// no tenant resolves.
	if token, ok := bearerToken(r); ok {
		if h.Validator == nil {
			http.Error(w, "authentication is not configured", http.StatusUnauthorized)
			return
		}
		claims, err := h.Validator.Validate(token)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		ctx := core.WithTenant(r.Context(), claims.Tenant)
		ctx = auth.WithClaims(ctx, claims)
		r = r.WithContext(ctx)
	}

	h.Relay.ServeHTTP(w, r)
}

// bearerToken extracts the access token from the Authorization header. It
// returns ("", false) when there is no Bearer credential to verify.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
