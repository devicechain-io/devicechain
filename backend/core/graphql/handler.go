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

// authPolicy decides how a handler authenticates a request before dispatching to
// its schema. The two policies map to the two token tiers (ADR-033):
//
//   - tenantPolicy (data plane): a bearer token is optional; when present it is
//     validated as a tenant access token and its tenant stamped into context. An
//     absent token runs unauthenticated so open entry points (the login mutation)
//     stay reachable; every tenant-scoped operation still fails closed at the DB.
//   - identityPolicy (admin plane): a bearer token is required and validated as an
//     instance-scoped identity token; its claims are attached without a tenant, so
//     admin operations run in the system context, across tenants.
type authPolicy struct {
	// required rejects a request that carries no bearer token (401).
	required bool
	// validate verifies a token of this policy's tier and returns its claims.
	validate func(*auth.Validator, string) (*auth.Claims, error)
	// setTenant stamps the claims' tenant into the request context (data plane).
	setTenant bool
}

var (
	tenantPolicy   = authPolicy{required: false, validate: (*auth.Validator).Validate, setTenant: true}
	identityPolicy = authPolicy{required: true, validate: (*auth.Validator).ValidateIdentity, setTenant: false}
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

	policy authPolicy
}

// Create new http handler for the data plane. gate may be nil only for a
// deliberately unauthenticated server (tests); production services pass the
// microservice readiness gate.
func NewHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *HttpHandler {
	return newHandler(schema, providers, gate, tenantPolicy)
}

// NewAdminHttpHandler creates the instance-scoped admin handler (ADR-033): it
// requires an identity-tier token and runs in the system context (no tenant).
// Resolvers still gate each operation on a specific system authority.
func NewAdminHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *HttpHandler {
	return newHandler(schema, providers, gate, identityPolicy)
}

func newHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate, policy authPolicy) *HttpHandler {
	return &HttpHandler{
		Schema:           schema,
		Relay:            &relay.Handler{Schema: schema},
		ContextProviders: providers,
		Gate:             gate,
		policy:           policy,
	}
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

	// Authenticate via the verified JWT (ADR-008), which replaces the former
	// trusted X-DC-Tenant gateway header: a verified token's claims cannot be
	// forged because the signature covers them. The policy decides whether a
	// token is required, which tier it is validated as, and whether its tenant is
	// stamped into context (see authPolicy). A *present* token is always verified
	// — an invalid one is rejected with 401, never silently ignored.
	token, ok := bearerToken(r)
	if !ok {
		if h.policy.required {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		// Data plane: run unauthenticated so open entry points (the login
		// mutation) stay reachable; tenant-scoped operations fail closed at the DB.
		h.Relay.ServeHTTP(w, r)
		return
	}

	validator := h.validator()
	if validator == nil {
		http.Error(w, "authentication is not available", http.StatusUnauthorized)
		return
	}
	claims, err := h.policy.validate(validator, token)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	if h.policy.setTenant {
		ctx = core.WithTenant(ctx, claims.Tenant)
	}
	ctx = auth.WithClaims(ctx, claims)
	h.Relay.ServeHTTP(w, r.WithContext(ctx))
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
