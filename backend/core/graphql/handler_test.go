// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// While the readiness gate is closed the validator is nil, so a presented bearer
// token is rejected with 401 rather than being silently trusted or passed
// through unauthenticated (ADR-022 decision 3 / ADR-015 fail-closed).
func TestHandlerRejectsTokenWhileGateClosed(t *testing.T) {
	h := NewHttpHandler(nil, map[ContextKey]interface{}{}, core.NewReadinessGate())

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// With no gate at all (deliberately unauthenticated server) a presented token is
// likewise rejected rather than trusted.
func TestHandlerRejectsTokenWithNoGate(t *testing.T) {
	h := NewHttpHandler(nil, map[ContextKey]interface{}{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
