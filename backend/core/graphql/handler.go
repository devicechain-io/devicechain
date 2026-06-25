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
	// Gate supplies the live JWT validator, which is bound late: a service starts
	// not-ready and the validator becomes available only once the auth bootstrap
	// succeeds (ADR-022 decision 3). While the gate is closed the validator is
	// nil, so a presented token is rejected (never silently trusted) and the
	// service's readiness probe keeps external traffic away in the first place.
	Gate *core.ReadinessGate
}

// Create new http handler. gate may be nil only for a deliberately
// unauthenticated server (tests); production services pass the microservice
// readiness gate.
func NewHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *HttpHandler {
	handler := &HttpHandler{
		Schema:           schema,
		Relay:            &relay.Handler{Schema: schema},
		ContextProviders: providers,
		Gate:             gate,
	}
	return handler
}

// validator returns the live validator, or nil when the gate is absent or still
// closed.
func (h *HttpHandler) validator() *auth.Validator {
	if h.Gate == nil {
		return nil
	}
	return h.Gate.Validator()
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
		validator := h.validator()
		if validator == nil {
			http.Error(w, "authentication is not available", http.StatusUnauthorized)
			return
		}
		claims, err := validator.Validate(token)
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
