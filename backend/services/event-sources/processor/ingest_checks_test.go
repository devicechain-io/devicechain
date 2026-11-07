// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/credcheck"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type verdictFn func(tenant string, p credcheck.Presented) credcheck.Verdict

func (f verdictFn) Verify(_ context.Context, tenant string, p credcheck.Presented) credcheck.Verdict {
	return f(tenant, p)
}

func post(es *HttpEventSource, tenant, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/inst-1/"+tenant+"/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)
	return rec
}

// An unknown tenant is a 401, nothing is published, and the refusal is counted by reason.
func TestHttpEvents_UnknownTenantIs401(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	var reasons []string
	es.UseIngestChecks(&IngestChecks{
		TenantUnknown: func(_ context.Context, tenant string) bool { return tenant == "typo" },
		Refused:       func(_, reason string) { reasons = append(reasons, reason) },
	})

	rec := post(es, "typo", canonicalMeasurementBody)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, dec.called, "a refused request must not be published")
	assert.Equal(t, []string{RefusedUnknownTenant}, reasons)
	assert.Empty(t, rec.Header().Get(EventIdHeader), "a 401 carries no receipt: nothing was received")

	// The same body to a tenant that exists is accepted.
	rec = post(es, "acme", canonicalMeasurementBody)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.True(t, dec.called)
}

// The 401 for an unknown tenant and for a bad credential are byte-identical: status, headers and
// body. This is the anti-enumeration property, and it is asserted on the whole response.
func TestHttpEvents_Unauthorized_IsIndistinguishableForEveryCause(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	es.UseIngestChecks(&IngestChecks{
		TenantUnknown: func(_ context.Context, tenant string) bool { return tenant == "typo" },
		Credential: verdictFn(func(string, credcheck.Presented) credcheck.Verdict {
			return credcheck.Rejected
		}),
	})

	tenantRefusal := post(es, "typo", canonicalMeasurementBody)
	credentialRefusal := post(es, "acme", canonicalMeasurementBody)

	require.Equal(t, http.StatusUnauthorized, tenantRefusal.Code)
	require.Equal(t, http.StatusUnauthorized, credentialRefusal.Code)
	assert.Equal(t, tenantRefusal.Body.String(), credentialRefusal.Body.String())
	assert.Equal(t, tenantRefusal.Header(), credentialRefusal.Header())
	assert.Equal(t, `{"error":"unauthorized"}`+"\n", tenantRefusal.Body.String())
	assert.NotEmpty(t, tenantRefusal.Header().Get("WWW-Authenticate"), "RFC 9110 requires a challenge on a 401")
}

// A credential the check rejects is a 401 and never published; the check is handed exactly what
// the body carried, and the tenant from the path.
func TestHttpEvents_RejectedCredentialIs401(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	var gotTenant string
	var got credcheck.Presented
	var reasons []string
	es.UseIngestChecks(&IngestChecks{
		Credential: verdictFn(func(tenant string, p credcheck.Presented) credcheck.Verdict {
			gotTenant, got = tenant, p
			return credcheck.Rejected
		}),
		Refused: func(_, reason string) { reasons = append(reasons, reason) },
	})
	body := `{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC",` +
		`"credentialId":"user-1","credentialSecret":"hunter2hunter2",` +
		`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`

	rec := post(es, "acme", body)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, dec.called)
	assert.Equal(t, "acme", gotTenant)
	assert.Equal(t, credcheck.Presented{Device: "sensor-001", Type: "MQTT_BASIC", Id: "user-1", Secret: "hunter2hunter2"}, got)
	assert.Equal(t, []string{RefusedCredential}, reasons)
	assert.NotContains(t, rec.Body.String(), "hunter2", "a response never echoes a secret")
}

// UNKNOWN and ACCEPTED are the same to the door: it carries on exactly as before the check.
func TestHttpEvents_UnknownAndAcceptedVerdictsCarryOn(t *testing.T) {
	for _, v := range []credcheck.Verdict{credcheck.Unknown, credcheck.Accepted} {
		es, dec, _ := newTestHttpSource(t, nil)
		es.UseIngestChecks(&IngestChecks{Credential: verdictFn(func(string, credcheck.Presented) credcheck.Verdict { return v })})
		rec := post(es, "acme", canonicalMeasurementBody)
		assert.Equal(t, http.StatusAccepted, rec.Code, "verdict %d", v)
		assert.True(t, dec.called)
	}
}

// A nil member is no check, and a source with no checks at all is the door as it was.
func TestHttpEvents_NoChecksIsTheOldDoor(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	assert.Equal(t, http.StatusAccepted, post(es, "acme", canonicalMeasurementBody).Code)
	assert.True(t, dec.called)

	es, dec, _ = newTestHttpSource(t, nil)
	es.UseIngestChecks(&IngestChecks{})
	assert.Equal(t, http.StatusAccepted, post(es, "acme", canonicalMeasurementBody).Code)
	assert.True(t, dec.called)
}

// The credential is checked AFTER the tenant's own ceiling: a request the tenant was not allowed
// to send is a 429 and never spends the check's budget.
func TestHttpEvents_CredentialIsCheckedAfterTheTenantCeiling(t *testing.T) {
	deny := func(string, string, time.Time, bool, Origin) bool { return false }
	es, _, _ := newTestHttpSource(t, deny)
	asked := false
	es.UseIngestChecks(&IngestChecks{Credential: verdictFn(func(string, credcheck.Presented) credcheck.Verdict {
		asked = true
		return credcheck.Rejected
	})})
	rec := post(es, "acme", canonicalMeasurementBody)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.False(t, asked)
}

// A malformed body is answered the same for a tenant that exists and one that does not: the
// refusal for the tenant comes after the decode, so the body is not an existence oracle.
func TestHttpEvents_MalformedBodyDoesNotRevealWhetherTheTenantExists(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	es.UseIngestChecks(&IngestChecks{TenantUnknown: func(_ context.Context, tenant string) bool { return tenant == "typo" }})
	a := post(es, "typo", `{"device":`)
	b := post(es, "acme", `{"device":`)
	assert.Equal(t, http.StatusBadRequest, a.Code)
	assert.Equal(t, a.Code, b.Code)
	assert.Equal(t, a.Body.String(), b.Body.String())

	c := post(es, "typo", `{"device":"d","eventType":"Measurement","payload":{}}`)
	d := post(es, "acme", `{"device":"d","eventType":"Measurement","payload":{}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, c.Code)
	assert.Equal(t, c.Code, d.Code)
	var cb, db unprocessableBody
	require.NoError(t, json.Unmarshal(c.Body.Bytes(), &cb))
	require.NoError(t, json.Unmarshal(d.Body.Bytes(), &db))
	assert.Equal(t, cb.Errors, db.Errors)
}

// 202 carries a receipt, it is fresh per request, and it is hex of the advertised width.
func TestHttpEvents_AcceptedCarriesAFreshEventId(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	a := post(es, "acme", canonicalMeasurementBody)
	b := post(es, "acme", canonicalMeasurementBody)
	require.Equal(t, http.StatusAccepted, a.Code)
	ida, idb := a.Header().Get(EventIdHeader), b.Header().Get(EventIdHeader)
	assert.Regexp(t, `^[0-9a-f]{32}$`, ida)
	assert.NotEqual(t, ida, idb)
}

// A request that is not accepted because the publish failed carries no receipt: a receipt is
// a promise that the request was accepted.
func TestHttpEvents_NoEventIdOnAPublishFailure(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	dec.publishErr = context.DeadlineExceeded
	rec := post(es, "acme", canonicalMeasurementBody)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Empty(t, rec.Header().Get(EventIdHeader))
}

// The 422 contract: a well-formed document the platform refuses answers 422 with a JSON Pointer
// that resolves to the offending value, and the receipt.
func TestHttpEvents_Unprocessable_NamesTheValue(t *testing.T) {
	cases := []struct {
		name, body, pointer string
	}{
		{"no entries wrapper", `{"device":"d","eventType":"Measurement","payload":{"temp":"1"}}`, "/payload/entries"},
		{"empty entry", `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}},{}]}}`, "/payload/entries/1/measurements"},
		{"alert without type", `{"device":"d","eventType":"Alert","payload":{"entries":[{"level":1}]}}`, "/payload/entries/0/type"},
		{"bad entry time", `{"device":"d","eventType":"Measurement","payload":{"entries":[{"occurredTime":"yesterday","measurements":{"a":"1"}}]}}`, "/payload/entries/0/occurredTime"},
		{"bad envelope time", `{"device":"d","eventType":"Measurement","occurredTime":"yesterday","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, "/occurredTime"},
		{"unknown type", `{"device":"d","eventType":"Telepathy","payload":{}}`, "/eventType"},
		{"location out of range", `{"device":"d","eventType":"Location","payload":{"entries":[{"latitude":"91","longitude":"0"}]}}`, "/payload/entries/0/latitude"},
		{"location missing", `{"device":"d","eventType":"Location","payload":{"entries":[{"latitude":"1"}]}}`, "/payload/entries/0/longitude"},
		{"wrong json type", `{"device":"d","eventType":"Alert","payload":{"entries":[{"type":"x","level":"high"}]}}`, "/payload/entries"},
		{"envelope field of the wrong type", `{"device":5,"eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}}]}}`, "/device"},
		{"payload that is not an object", `{"device":"d","eventType":"Measurement","payload":"temp=1"}`, "/payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			es, dec, fail := newTestHttpSource(t, nil)
			rec := post(es, "acme", tc.body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.False(t, dec.called)
			assert.True(t, fail.called, "the failure still goes to the failed-decode path")
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var body unprocessableBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, "unprocessable_event", body.Error)
			require.Len(t, body.Errors, 1)
			assert.Equal(t, tc.pointer, body.Errors[0].Pointer)
			assert.NotEmpty(t, body.Errors[0].Detail)
			assert.Regexp(t, `^[0-9a-f]{32}$`, body.EventId)
			assert.Equal(t, body.EventId, rec.Header().Get(EventIdHeader))
		})
	}
}

// Not JSON at all stays a 400: there is nothing in it to point at.
func TestHttpEvents_MalformedJSONStays400(t *testing.T) {
	for _, body := range []string{``, `{`, `not json`, `{"device":"d",}`} {
		es, _, _ := newTestHttpSource(t, nil)
		assert.Equal(t, http.StatusBadRequest, post(es, "acme", body).Code, "%q", body)
	}
}

// The pointers the decoder names resolve in the document that was posted (RFC 6901), for the
// shapes that name an entry. A pointer that did not resolve would be worse than none.
func TestDecodePointersResolveInThePostedDocument(t *testing.T) {
	body := `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{"a":"1"}},{"measurements":{}}]}}`
	_, _, err := NewJsonDecoder(map[string]string{}).Decode([]byte(body), time.Time{})
	require.Error(t, err)
	p := ProblemOf(err)
	var doc any
	require.NoError(t, json.Unmarshal([]byte(body), &doc))
	assert.True(t, resolves(doc, p.Pointer), "pointer %q must resolve", p.Pointer)
}

func resolves(doc any, pointer string) bool {
	if pointer == "" {
		return true
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) {
				return false
			}
			cur = v[i]
		default:
			return false
		}
	}
	return true
}

func TestEscapePointerToken(t *testing.T) {
	assert.Equal(t, "a~1b~0c", escapePointerToken("a/b~c"))
}

func TestProblemOf_UnclassifiedErrorAddressesTheWholeDocument(t *testing.T) {
	assert.Equal(t, DecodeProblem{Pointer: "", Detail: "boom"}, ProblemOf(errors.New("boom")))
}

func TestAtPointer_KeepsTheMoreSpecificPointer(t *testing.T) {
	inner := atPointer("/payload/entries/0/type", errors.New("x"))
	assert.Equal(t, "/payload/entries/0/type", ProblemOf(atPointer("/payload/entries", inner)).Pointer)
	assert.NoError(t, atPointer("/x", nil))
}

func TestPointerError_MessageIsTheWrappedErrorsOwn(t *testing.T) {
	assert.Equal(t, "boom", atPointer("/x", errors.New("boom")).Error())
}

// The reading limit's refusal points at the entries, where the readings are.
func TestHttpEvents_Unprocessable_TooManyReadingsPointsAtTheEntries(t *testing.T) {
	var readings strings.Builder
	for i := 0; i < 257; i++ {
		if i > 0 {
			readings.WriteString(",")
		}
		readings.WriteString(`"m` + strconv.Itoa(i) + `":"1"`)
	}
	body := `{"device":"d","eventType":"Measurement","payload":{"entries":[{"measurements":{` + readings.String() + `}}]}}`
	es, dec, _ := newTestHttpSource(t, nil)
	rec := post(es, "acme", body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.False(t, dec.called)
	var got unprocessableBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Errors, 1)
	assert.Equal(t, "/payload/entries", got.Errors[0].Pointer)
}

// A refusal's log line is sampled to one a second per reason, so a flood of refusals cannot flood
// the log, and one reason's flood does not hide another's first.
func TestSampler_OnePerIntervalPerKey(t *testing.T) {
	s := &sampler{interval: time.Second, last: map[string]time.Time{}}
	t0 := time.Unix(1_700_000_000, 0)
	assert.True(t, s.allow("a", t0))
	assert.False(t, s.allow("a", t0.Add(999*time.Millisecond)))
	assert.True(t, s.allow("b", t0.Add(time.Millisecond)), "another reason is its own key")
	assert.True(t, s.allow("a", t0.Add(time.Second)))
}

// A refusal is counted once, by source and reason, whatever the sampled log does.
func TestHttpEvents_RefusalIsCountedEveryTimeEvenWhenTheLogIsSampled(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	n := map[string]int{}
	es.UseIngestChecks(&IngestChecks{
		TenantUnknown: func(context.Context, string) bool { return true },
		Refused:       func(source, reason string) { n[source+"/"+reason]++ },
	})
	for i := 0; i < 5; i++ {
		post(es, "typo", canonicalMeasurementBody)
	}
	assert.Equal(t, 5, n["http-test/"+RefusedUnknownTenant])
}
