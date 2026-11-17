// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHttpEvents_FailedDecodeTenantIsolation(t *testing.T) {
	for _, body := range []string{"not json", `{"device":"d","eventType":"Measurement","payload":{}}`} {
		t.Run(body, func(t *testing.T) {
			// The response contract predates tenant checks. Only its per-request receipt varies.
			responseBody := func(rec *httptest.ResponseRecorder) string {
				if receipt := rec.Header().Get(EventIdHeader); receipt != "" {
					return strings.ReplaceAll(rec.Body.String(), receipt, "RECEIPT")
				}
				return rec.Body.String()
			}
			baseline, _, _ := newTestHttpSource(t, nil)
			want := post(baseline, "acme", body)
			require.Equal(t, http.StatusBadRequest, want.Code)
			for _, tc := range []struct {
				name    string
				checks  *IngestChecks
				unknown bool
			}{
				{"unknown tenant", &IngestChecks{TenantUnknown: func(context.Context, string) bool { return true }}, true},
				{"known tenant", &IngestChecks{TenantUnknown: func(context.Context, string) bool { return false }}, false},
				{"nil checks", nil, false},
				{"unavailable tenant check", &IngestChecks{}, false},
				// An unavailable lookup reports false under the TenantUnknown contract.
				{"lookup cannot answer", &IngestChecks{TenantUnknown: func(context.Context, string) bool { return false }}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					es, decoded, _ := newTestHttpSource(t, nil)
					var failures int
					es.failed = func(source, tenant string, raw []byte, err error) error {
						failures++
						assert.Equal(t, es.Id, source)
						assert.Equal(t, "target", tenant)
						assert.Equal(t, body, string(raw))
						assert.Error(t, err)
						return nil
					}
					var reasons []string
					if tc.checks != nil {
						tc.checks.Refused = func(source, reason string) {
							assert.Equal(t, es.Id, source)
							reasons = append(reasons, reason)
						}
					}
					es.UseIngestChecks(tc.checks)
					got := post(es, "target", body)
					assert.Equal(t, want.Code, got.Code)
					assert.Equal(t, responseBody(want), responseBody(got))
					assert.Equal(t, want.Header().Get("Content-Type"), got.Header().Get("Content-Type"))
					assert.False(t, decoded.called)
					if tc.unknown {
						assert.Zero(t, failures, "unknown tenants must not write to the shared failed-decode stream")
						assert.Equal(t, []string{RefusedUnknownTenant}, reasons)
					} else {
						assert.Equal(t, 1, failures)
						assert.Empty(t, reasons)
					}
				})
			}
		})
	}
}
