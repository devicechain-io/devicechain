// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
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
	// ContextNatsKey carries the service's *messaging.NatsManager so subscription
	// resolvers can open a live tenant-scoped feed (SubscribeLive). Stored as an
	// opaque value here to avoid a core/graphql → messaging import.
	ContextNatsKey ContextKey = "nats"
)

// authPolicy decides how a handler authenticates a request before dispatching to
// its schema. Its authenticate function verifies a present bearer token and
// returns the claims plus the tenant to stamp into context ("" for none). The two
// policies map to the request tiers:
//
//   - tenantPolicy (data plane): a bearer token is optional; when present it is
//     admitted as either a tenant *access* token (tenant taken from the signed
//     claim) or a *service* token (ADR-044 amendment — tenant taken from the
//     verified caller's explicit header). An absent token runs unauthenticated so
//     open entry points (the login mutation) stay reachable; every tenant-scoped
//     operation still fails closed at the DB.
//   - identityPolicy (admin plane): a bearer token is required and validated as an
//     instance-scoped identity token; its claims are attached without a tenant, so
//     admin operations run in the system context, across tenants.
type authPolicy struct {
	// required rejects a request that carries no bearer token (401).
	required bool
	// authenticate verifies the token for this policy's tier and returns its claims
	// and the tenant to stamp into context (empty stamps none). header resolves a
	// request header by name — used to read the service-token tenant; transports
	// with no such header (the WS subscription path) pass a resolver returning "",
	// which makes service tokens inapplicable there (their tenant resolves empty
	// and is rejected), leaving access/identity tokens working as before.
	authenticate func(v *auth.Validator, token string, header func(string) string) (*auth.Claims, string, error)
}

var (
	tenantPolicy   = authPolicy{required: false, authenticate: authenticateDataPlane}
	identityPolicy = authPolicy{required: true, authenticate: authenticateIdentity}
)

// authenticateDataPlane admits either tier the data plane accepts. It verifies the
// signature once (Parse), then branches on the token type: an access token supplies
// its own signed tenant; a service token (a verified machine caller) supplies the
// tenant via the ServiceTenantHeader — honored only *after* the signature checks
// out, so a client cannot forge tenancy with the header. Refresh/identity tokens
// are refused here.
func authenticateDataPlane(v *auth.Validator, token string, header func(string) string) (*auth.Claims, string, error) {
	claims, err := v.Parse(token)
	if err != nil {
		return nil, "", err
	}
	switch claims.TokenType {
	case auth.TokenTypeAccess:
		if claims.Tenant == "" {
			return nil, "", fmt.Errorf("auth: access token has no tenant claim")
		}
		return claims, claims.Tenant, nil
	case auth.TokenTypeService:
		tenant := header(auth.ServiceTenantHeader)
		if tenant == "" {
			return nil, "", fmt.Errorf("auth: service token presented without a %s header", auth.ServiceTenantHeader)
		}
		// The tenant flows into per-tenant NATS subject construction
		// ({instanceId}.{tenant}.…) downstream, where '.', '*', '>' are structural.
		// Enforce the token grammar
		// here so a malformed tenant is rejected at the boundary rather than
		// splicing into a subject — belt-and-suspenders even though only a verified
		// service token reaches this branch.
		if err := core.ValidateToken(tenant); err != nil {
			return nil, "", fmt.Errorf("auth: invalid %s value: %w", auth.ServiceTenantHeader, err)
		}
		return claims, tenant, nil
	default:
		return nil, "", fmt.Errorf("auth: token type %q is not accepted on the data plane", claims.TokenType)
	}
}

// authenticateIdentity validates an instance-scoped identity token and stamps no
// tenant (the admin plane runs in the system context, across tenants).
func authenticateIdentity(v *auth.Validator, token string, _ func(string) string) (*auth.Claims, string, error) {
	claims, err := v.ValidateIdentity(token)
	if err != nil {
		return nil, "", err
	}
	return claims, "", nil
}

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
	// Cap the request body before anything reads it (ADR-029): the relay's JSON
	// decode buffers the whole envelope — query + variables — into memory before
	// the query-length ceiling can reject an oversized query, so without this a
	// multi-hundred-MB body is a cheap memory-exhaustion vector. MaxBytesReader
	// stops the read at the ceiling and surfaces a decode error instead.
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes())
	}

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
	claims, tenant, err := h.policy.authenticate(validator, token, r.Header.Get)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	if tenant != "" {
		ctx = core.WithTenant(ctx, tenant)
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
