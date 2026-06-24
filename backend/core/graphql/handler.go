// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"net/http"

	"github.com/devicechain-io/dc-microservice/core"
	graphql "github.com/graph-gophers/graphql-go"

	"github.com/graph-gophers/graphql-go/relay"
)

// TenantHeader carries the request tenant on the API path.
//
// TEMPORARY trusted-source seam (ADR-008): until the verified JWT tenant claim
// lands, the tenant is read from this header. It MUST only ever be set by the
// gateway/ingress (which authenticates the caller and resolves the tenant); it
// must NEVER be trusted from an untrusted/external client. This will be
// replaced by the JWT-derived tenant claim. Enforcement itself is fail-closed
// at the DB callback layer, so an absent header is not rejected here — a
// tenant-scoped query simply fails closed downstream.
const TenantHeader = "X-DC-Tenant"

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
}

// Create new http handler.
func NewHttpHandler(schema *graphql.Schema, providers map[ContextKey]interface{}) *HttpHandler {
	handler := &HttpHandler{
		Schema:           schema,
		Relay:            &relay.Handler{Schema: schema},
		ContextProviders: providers,
	}
	return handler
}

// Handles http request processing.
func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for key, value := range h.ContextProviders {
		r = r.WithContext(context.WithValue(r.Context(), key, value))
	}

	// Attach the request tenant to context when supplied by the trusted gateway.
	// Do NOT hard-reject when absent: enforcement is fail-closed at the DB
	// callback layer, where tenant-scoped operations without a tenant in context
	// are rejected.
	if tenant, ok := tenantFromRequest(r); ok {
		r = r.WithContext(core.WithTenant(r.Context(), tenant))
	}

	h.Relay.ServeHTTP(w, r)
}

// tenantFromRequest resolves the request tenant. This is the single seam to
// change when the verified JWT tenant claim (ADR-008) replaces the temporary
// X-DC-Tenant header (see TenantHeader): ServeHTTP itself stays untouched.
func tenantFromRequest(r *http.Request) (string, bool) {
	if tenant := r.Header.Get(TenantHeader); tenant != "" {
		return tenant, true
	}
	return "", false
}
