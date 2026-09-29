// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 An event carries at most 256 readings, and an HTTP client that sends more is told so
// with a 400 — through the SHIPPED listener and the real JSON decoder, not the decoder alone,
// because the status code is what the device actually reads. Before the platform limit the
// JSON path refused only above an operator ceiling that defaulted to 1000, so 257 readings
// were answered 202.
//
// Every kind is its own sub-test, and the wide entry is one too: a single measurement entry
// holding 257 metric keys is 257 stored readings, and a limit that counted entries would let
// it through.
func TestHttpRefusesAnEventOverTheReadingLimitWith400(t *testing.T) {
	cases := map[string]func(n int) string{
		"Measurement": func(n int) string { return entriesBody("Measurement", n) },
		"Location":    func(n int) string { return entriesBody("Location", n) },
		"Alert":       func(n int) string { return entriesBody("Alert", n) },
		"WideEntry":   wideEntryBody,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			es, dec, fail := newTestHttpSource(t, nil)
			req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body(257)))
			rec := httptest.NewRecorder()
			es.handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, "257 readings is over the limit of 256 and must be refused")
			assert.False(t, dec.called, "a refused event must never be published")
			require.True(t, fail.called, "the refusal must route to the failed-decode path")
			assert.True(t, errors.Is(fail.err, model.ErrTooManyReadings),
				"the refusal must carry the sentinel the refusal counter keys on, got %v", fail.err)
			for _, want := range []string{"257 readings", "limit of 256 readings per event"} {
				assert.Contains(t, rec.Body.String(), want, "the 400 body tells the device the count and the limit")
			}

			// The counterweight: AT the limit is accepted, so the refusal above is the limit
			// and not a handler that refuses everything this size.
			es, dec, fail = newTestHttpSource(t, nil)
			req = httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body(256)))
			rec = httptest.NewRecorder()
			es.handler().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusAccepted, rec.Code, "256 readings is AT the limit and must be accepted")
			assert.True(t, dec.called)
			assert.False(t, fail.called)
		})
	}
}
