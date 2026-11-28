// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	automationCredential = "automation-credential-0123456789abcdefghijklmnopq" // ≥ MinAreaCredentialLen
	otherCredential      = "another-area-credential-0123456789abcdefghijklmno"
)

// The ceiling the area-bound tests judge against: automation may have device:read and tenant:read,
// as the design gives it. It stands in for auth.CheckServiceCeiling only because automation's
// ceiling entry lands with its mint site; TestAreaMintProductionCeilingRefusesAnUnlistedArea covers
// the production table.
func automationCeiling(subject string, authorities []string) auth.ServiceCeilingResult {
	res := auth.ServiceCeilingResult{Caller: auth.UnknownCallerLabel, Verdict: auth.CeilingUnknownCaller}
	if subject != "automation" && subject != "spare" {
		return res
	}
	res.Caller = subject
	wildcard := false
	for _, a := range authorities {
		switch auth.Authority(a) {
		case auth.DeviceRead, auth.TenantRead:
			continue
		case auth.AuthorityAll:
			wildcard = true
		}
		res.Offending = append(res.Offending, a)
	}
	switch {
	case wildcard:
		res.Verdict = auth.CeilingWildcard
	case len(res.Offending) > 0:
		res.Verdict = auth.CeilingOver
	default:
		res.Verdict = auth.CeilingWithin
	}
	return res
}

type areaMintFixture struct {
	creds   map[string]string
	issued  []string // subjects issue was called with
	metrics *AreaMintMetrics
	check   func(string, []string) auth.ServiceCeilingResult
	v       *auth.Validator
	issue   func(subject string, authorities []string) (auth.IssuedToken, error)
}

func newAreaMintFixture(t *testing.T) *areaMintFixture {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	iss := auth.NewIssuer(key, "test", time.Minute, time.Hour)
	f := &areaMintFixture{
		creds:   map[string]string{"automation": automationCredential, "spare": otherCredential},
		metrics: NewAreaMintMetrics(prometheus.NewRegistry(), "usermanagement"),
		check:   automationCeiling,
		v:       auth.NewValidator(&key.PublicKey),
	}
	f.issue = func(subject string, authorities []string) (auth.IssuedToken, error) {
		f.issued = append(f.issued, subject)
		return iss.IssueService(subject, authorities, "jti-area")
	}
	return f
}

func (f *areaMintFixture) credential(area string) (string, error) {
	c, ok := f.creds[area]
	if !ok {
		return "", os.ErrNotExist
	}
	return c, nil
}

func (f *areaMintFixture) handler() http.Handler {
	return AreaServiceTokenHandler([]string{"automation", "spare"}, f.credential, f.issue, f.check, f.metrics)
}

func (f *areaMintFixture) post(t *testing.T, presented, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, auth.AreaServiceTokenPath, strings.NewReader(body))
	if presented != "" {
		req.Header.Set(auth.AreaCredentialHeader, presented)
	}
	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, req)
	return rec
}

func (f *areaMintFixture) count(area, result string) float64 {
	return testutil.ToFloat64(f.metrics.counter(area, result))
}

// §7.3 property 1: with the area's own credential, a read inside its ceiling is issued, under the
// area's own name, and nothing else is.
func TestAreaMintIssuesTheCeilingUnderTheMatchedArea(t *testing.T) {
	f := newAreaMintFixture(t)
	rec := f.post(t, automationCredential, `{"authorities":["device:read","tenant:read"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp auth.ServiceTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := f.v.ValidateService(resp.Token)
	if err != nil {
		t.Fatalf("the minted token does not validate as a service token: %v", err)
	}
	if claims.Username != "automation" {
		t.Fatalf("subject = %q, want automation (the area whose credential matched)", claims.Username)
	}
	if !claims.HasAuthority(auth.DeviceRead) || claims.HasAuthority(auth.DeviceWrite) {
		t.Fatalf("authorities = %v", claims.Authorities)
	}
	if got := f.count("automation", AreaMintIssued); got != 1 {
		t.Fatalf("issued count = %v, want 1", got)
	}

	// The other configured area's credential yields that area, not automation.
	rec = f.post(t, otherCredential, `{"authorities":["device:read"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.issued[len(f.issued)-1] != "spare" {
		t.Fatalf("issued under %q, want spare", f.issued[len(f.issued)-1])
	}
}

// §7.3 property 1, the three refusals: a write authority and the super-authority are 403, and a body
// naming a subject is 400 — whatever subject it names. Nothing is issued for any of them.
func TestAreaMintRefusesEveryWideningRequest(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
		result     string
	}{
		{"a write authority", `{"authorities":["device:write"]}`, http.StatusForbidden, AreaMintOverCeiling},
		{"a write beside a read", `{"authorities":["device:read","device:write"]}`, http.StatusForbidden, AreaMintOverCeiling},
		{"a command authority", `{"authorities":["command:write"]}`, http.StatusForbidden, AreaMintOverCeiling},
		{"the fleet authority", `{"authorities":["fleet:operate"]}`, http.StatusForbidden, AreaMintOverCeiling},
		{"the super-authority", `{"authorities":["*"]}`, http.StatusForbidden, AreaMintWildcard},
		{"the super-authority beside a read", `{"authorities":["device:read","*"]}`, http.StatusForbidden, AreaMintWildcard},
		{"a named subject", `{"subject":"lwm2m-ingest","authorities":["device:read"]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"its own name as subject", `{"subject":"automation","authorities":["device:read"]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"an empty subject", `{"subject":"","authorities":["device:read"]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"an unknown field", `{"authorities":["device:read"],"tenant":"t1"}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"a second document", `{"authorities":["device:read"]}{"authorities":["device:write"]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"no authorities", `{"authorities":[]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"an unknown authority", `{"authorities":["device:raed"]}`, http.StatusBadRequest, AreaMintInvalidRequest},
		{"not JSON", `device:write`, http.StatusBadRequest, AreaMintInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAreaMintFixture(t)
			rec := f.post(t, automationCredential, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if len(f.issued) != 0 {
				t.Fatalf("a token was issued for %v", f.issued)
			}
			if got := f.count("automation", tc.result); got != 1 {
				t.Fatalf("%s count = %v, want 1", tc.result, got)
			}
			if got := f.count("automation", AreaMintIssued); got != 0 {
				t.Fatalf("issued count = %v, want 0", got)
			}
		})
	}
}

// The credential is the only thing that authenticates: a wrong, empty, short or other-endpoint
// credential gets nothing, and the shared service secret header is not a credential here.
func TestAreaMintRefusesAnythingButAnAreaCredential(t *testing.T) {
	body := `{"authorities":["device:read"]}`
	for name, presented := range map[string]string{
		"none":                     "",
		"wrong":                    "not-the-credential-0123456789abcdefghijklmnopqrs",
		"a prefix of the real one": automationCredential[:len(automationCredential)-1],
		"the real one plus a byte": automationCredential + "x",
	} {
		t.Run(name, func(t *testing.T) {
			f := newAreaMintFixture(t)
			rec := f.post(t, presented, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(f.issued) != 0 {
				t.Fatal("a token was issued")
			}
			if got := f.count(auth.UnknownCallerLabel, AreaMintBadCredential); got != 1 {
				t.Fatalf("bad_credential count = %v, want 1", got)
			}
		})
	}

	t.Run("the shared secret header is ignored", func(t *testing.T) {
		f := newAreaMintFixture(t)
		req := httptest.NewRequest(http.MethodPost, auth.AreaServiceTokenPath, strings.NewReader(body))
		req.Header.Set(auth.ServiceSecretHeader, automationCredential)
		rec := httptest.NewRecorder()
		f.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})

	t.Run("non-POST", func(t *testing.T) {
		f := newAreaMintFixture(t)
		req := httptest.NewRequest(http.MethodGet, auth.AreaServiceTokenPath, nil)
		req.Header.Set(auth.AreaCredentialHeader, automationCredential)
		rec := httptest.NewRecorder()
		f.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})
}

// A configured credential that is missing, unreadable, empty or too short is not a credential: it
// can match nothing, the empty header least of all. With none configured the endpoint is off.
func TestAreaMintFailsClosedOnUnusableConfiguredCredentials(t *testing.T) {
	body := `{"authorities":["device:read"]}`

	t.Run("an empty configured credential does not match an empty header", func(t *testing.T) {
		f := newAreaMintFixture(t)
		f.creds = map[string]string{"automation": ""}
		rec := f.post(t, "", body)
		if rec.Code == http.StatusOK || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})

	t.Run("a short configured credential matches nothing, not even itself", func(t *testing.T) {
		f := newAreaMintFixture(t)
		short := strings.Repeat("a", auth.MinAreaCredentialLen-1)
		f.creds = map[string]string{"automation": short, "spare": otherCredential}
		rec := f.post(t, short, body)
		if rec.Code != http.StatusUnauthorized || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})

	t.Run("an unreadable credential is skipped, the readable one still works", func(t *testing.T) {
		f := newAreaMintFixture(t)
		delete(f.creds, "spare")
		if rec := f.post(t, automationCredential, body); rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("none configured is 503", func(t *testing.T) {
		f := newAreaMintFixture(t)
		f.creds = map[string]string{}
		rec := f.post(t, automationCredential, body)
		if rec.Code != http.StatusServiceUnavailable || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})

	t.Run("a credential read error other than absence is still no credential", func(t *testing.T) {
		f := newAreaMintFixture(t)
		h := AreaServiceTokenHandler([]string{"automation"}, func(string) (string, error) {
			return automationCredential, errors.New("permission denied")
		}, f.issue, f.check, f.metrics)
		req := httptest.NewRequest(http.MethodPost, auth.AreaServiceTokenPath, strings.NewReader(body))
		req.Header.Set(auth.AreaCredentialHeader, automationCredential)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || len(f.issued) != 0 {
			t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
		}
	})
}

// The production ceiling table is what the endpoint is wired to. An area-bound area with no entry
// in it (automation, until its mint site lands) has an empty ceiling: everything is refused.
func TestAreaMintProductionCeilingRefusesAnUnlistedArea(t *testing.T) {
	f := newAreaMintFixture(t)
	f.check = auth.CheckServiceCeiling
	if _, listed := auth.ServiceCeiling("spare"); listed {
		t.Fatal("fixture area unexpectedly in the production table")
	}
	rec := f.post(t, otherCredential, `{"authorities":["device:read"]}`)
	if rec.Code != http.StatusForbidden || len(f.issued) != 0 {
		t.Fatalf("code=%d issued=%v", rec.Code, f.issued)
	}
}

// The credential never reaches a response, a metric label or the log — on any branch.
func TestAreaMintNeverEchoesTheCredential(t *testing.T) {
	logs := logSink.Capture(t)

	f := newAreaMintFixture(t)
	bodies := []string{
		`{"authorities":["device:read"]}`,
		`{"authorities":["device:write"]}`,
		`{"authorities":["*"]}`,
		`{"subject":"x","authorities":["device:read"]}`,
		`garbage`,
	}
	for _, presented := range []string{automationCredential, automationCredential + "x"} {
		for _, b := range bodies {
			rec := f.post(t, presented, b)
			if strings.Contains(rec.Body.String(), automationCredential) {
				t.Fatalf("the credential is in a response body: %s", rec.Body.String())
			}
			for k, vs := range rec.Header() {
				for _, v := range vs {
					if strings.Contains(v, automationCredential) {
						t.Fatalf("the credential is in response header %s", k)
					}
				}
			}
		}
	}
	if strings.Contains(logs.String(), automationCredential) || strings.Contains(logs.String(), otherCredential) {
		t.Fatalf("a credential reached the log:\n%s", logs.String())
	}
	if logs.String() == "" {
		t.Fatal("no refusal was logged at all, so this test read nothing")
	}
}

// Every (area, result) series exists at zero before the first request, so the alert's expression is
// defined on a quiet instance.
func TestAreaMintMetricsArePreSeeded(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewAreaMintMetrics(reg, "usermanagement")
	n, err := testutil.GatherAndCount(reg, "devicechain_usermanagement_area_mint_requests_total")
	if err != nil {
		t.Fatal(err)
	}
	want := (len(auth.AreaBoundCallerNames()) + 1) * len(AreaMintResults)
	if n != want {
		t.Fatalf("pre-seeded %d series, want %d", n, want)
	}
}

// Two areas configured with ONE credential: the request is refused, never resolved to either area
// (not the first listed, not the first sorted), and nothing is issued.
func TestAreaMintRefusesAnAmbiguousCredential(t *testing.T) {
	for _, order := range [][]string{{"automation", "spare"}, {"spare", "automation"}} {
		f := newAreaMintFixture(t)
		f.creds = map[string]string{"automation": automationCredential, "spare": automationCredential}
		h := AreaServiceTokenHandler(order, f.credential, f.issue, f.check, f.metrics)
		req := httptest.NewRequest(http.MethodPost, auth.AreaServiceTokenPath, strings.NewReader(`{"authorities":["device:read"]}`))
		req.Header.Set(auth.AreaCredentialHeader, automationCredential)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || len(f.issued) != 0 {
			t.Fatalf("order %v: code=%d issued=%v", order, rec.Code, f.issued)
		}
		if strings.Contains(rec.Body.String(), automationCredential) {
			t.Fatal("the credential is in the response")
		}
	}
}

// The two sides of the area-bound mint apply the same length floor.
func TestAreaCredentialFloorsAgree(t *testing.T) {
	if auth.MinAreaCredentialLen != credential.MinSharedCredentialLen {
		t.Fatalf("auth.MinAreaCredentialLen = %d, credential.MinSharedCredentialLen = %d",
			auth.MinAreaCredentialLen, credential.MinSharedCredentialLen)
	}
}
