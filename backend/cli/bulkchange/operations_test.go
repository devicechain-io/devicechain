// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// Exercise real authentication, schema negotiation and GraphQL error decoding.
func testAPI(t *testing.T, schema string, handle func(string, map[string]any) any, probe ...func() string) API {
	t.Helper()
	discoveries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			discoveries++
			if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("Accept") != "application/vnd.devicechain.schema+json" {
				t.Error("capability probe lacks authenticated discovery headers")
			}
			servedSchema := schema
			if discoveries > 1 && len(probe) != 0 {
				servedSchema = probe[0]()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": servedSchema})
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		var response any
		switch {
		case strings.Contains(body.Query, "login("):
			response = map[string]any{"data": map[string]any{"login": map[string]any{"identityToken": "identity", "expiresAt": expires}}}
		case strings.Contains(body.Query, "selectTenant("):
			response = map[string]any{"data": map[string]any{"selectTenant": map[string]any{"accessToken": "access", "expiresAt": expires}}}
		default:
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("missing tenant authentication")
			}
			if strings.Contains(body.Query, "__type(") {
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "introspection is disabled"}}})
				return
			}
			response = handle(body.Query, body.Variables)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(srv.Close)
	client := userclient.NewTenantSession(srv.Client(), srv.URL, "email", "password", "tenant").WithSchemaNegotiation(nil)
	return API{Client: NewClient(client), Endpoint: srv.URL, PollInterval: time.Millisecond}
}

func data(key string, value any) any { return map[string]any{"data": map[string]any{key: value}} }

func TestStatusEveryExitCodeAndWatchEvidence(t *testing.T) {
	for _, tc := range []struct {
		dispatch, evidence string
		code               int
	}{
		{"DISPATCHED", "CLOSED", 0}, {"DISPATCHED", "OPEN", 1},
		{"PAUSED", "OPEN", 3}, {"PAUSED", "CLOSED", 3},
		{"ABORTED", "OPEN", 4}, {"ABORTED", "CLOSED", 4},
		{"RUNNING", "OPEN", 1}, {"READY", "OPEN", 1},
		{"PLANNING", "OPEN", 1}, {"FUTURE", "OPEN", 1},
		{"DISPATCHED", "FUTURE", 1},
	} {
		t.Run(tc.dispatch+"/"+tc.evidence, func(t *testing.T) {
			a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
				return data("change", Change{Token: "t", Dispatch: tc.dispatch, Evidence: tc.evidence})
			})
			var out bytes.Buffer
			err := a.Status(t.Context(), "t", &out, false, true)
			code := 0
			if err != nil {
				var status *StatusError
				if !errors.As(err, &status) {
					t.Fatal(err)
				}
				code = status.ExitCode()
			}
			if code != tc.code || !json.Valid(out.Bytes()) {
				t.Fatalf("code=%d want=%d err=%v out=%s", code, tc.code, err, out.String())
			}
		})
	}
	reads := 0
	a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
		reads++
		evidence := "OPEN"
		if reads == 3 {
			evidence = "CLOSED"
		}
		return data("change", Change{Token: "t", Dispatch: "DISPATCHED", Evidence: evidence})
	})
	var out bytes.Buffer
	if err := a.Status(t.Context(), "t", &out, true, true); err != nil || reads != 3 {
		t.Fatalf("watch stopped before evidence closed: reads=%d err=%v", reads, err)
	}
	dec := json.NewDecoder(&out)
	for _, evidence := range []string{"OPEN", "OPEN", "CLOSED"} {
		var c Change
		if err := dec.Decode(&c); err != nil || c.Evidence != evidence {
			t.Fatalf("watch lost evidence state: %+v %v", c, err)
		}
	}
}

func TestWatchStopsOnPauseAbortAndCancellation(t *testing.T) {
	for _, dispatch := range []string{"PAUSED", "ABORTED", "RUNNING"} {
		t.Run(dispatch, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := 0
			a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
				reads++
				if dispatch == "RUNNING" {
					cancel()
				}
				return data("change", Change{Token: "t", Dispatch: dispatch, Evidence: "OPEN"})
			})
			err := a.Status(ctx, "t", io.Discard, true, false)
			if err == nil || reads != 1 {
				t.Fatalf("watch did not stop: %v reads=%d", err, reads)
			}
		})
	}
}

func TestUnservedAndUpgradingNeverBecomeNotFound(t *testing.T) {
	for _, served := range []bool{false, true} {
		t.Run(map[bool]string{false: "unserved", true: "upgrading"}[served], func(t *testing.T) {
			schema := unservedSDL(t)
			if served {
				schema = bulkChangeTestSDL(t)
			}
			probes, operations := 0, 0
			a := testAPI(t, schema, func(_ string, _ map[string]any) any {
				operations++
				return map[string]any{"errors": []any{map[string]any{"message": `Cannot query field "bulkChange" on type "Query".`}}}
			}, func() string {
				probes++
				return schema
			})
			_, err := a.Get(t.Context(), "missing")
			want := "this instance does not serve bulk changes"
			if served {
				want = "still upgrading; retry"
			}
			if err == nil || err.Error() != want || probes != 1 || operations != map[bool]int{false: 0, true: 1}[served] {
				t.Fatalf("err=%v probes=%d operations=%d", err, probes, operations)
			}
		})
	}
}

func TestMissingTokenAndQueryError(t *testing.T) {
	calls := 0
	a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
		calls++
		return data("change", nil)
	})
	if _, err := a.Get(t.Context(), ""); err == nil || calls != 0 {
		t.Fatal("absent argument was sent to server")
	}
	if _, err := a.Get(t.Context(), "missing"); err == nil || !strings.Contains(err.Error(), "token not found") {
		t.Fatalf("missing served token: %v", err)
	}
	if _, err := a.Lines(t.Context(), "missing", "", ""); err == nil {
		t.Fatal("missing token became successful empty lines")
	}
	a = testAPI(t, bulkChangeTestSDL(t), func(_ string, _ map[string]any) any {
		return map[string]any{"errors": []any{map[string]any{"message": "denied", "extensions": map[string]any{"code": "FORBIDDEN"}}}}
	}, func() string {
		t.Error("ordinary authorization failure triggered a version probe")
		return bulkChangeTestSDL(t)
	})
	if _, err := a.Get(t.Context(), "t"); !userclient.AllHaveCode(err, "FORBIDDEN") {
		t.Fatalf("authorization error was masked by a probe: %v", err)
	}
}

func TestPlanCreateAndControlUseServerDigests(t *testing.T) {
	var documents []string
	a := testAPI(t, bulkChangeTestSDL(t), func(query string, vars map[string]any) any {
		documents = append(documents, query)
		switch query {
		case planQuery:
			if vars["request"].(map[string]any)["token"] != "chosen" {
				t.Error("request lost token")
			}
			return data("plan", Plan{ContractDigest: "server-plan-digest"})
		case resumePreviewQuery:
			return data("preview", map[string]any{"contractDigest": "opaque-server-resume-digest", "withinContract": true})
		case startQuery:
			if vars["digest"] != "reviewed-server-digest" {
				t.Error("start did not forward exact reviewed digest")
			}
		case resumeQuery:
			if vars["digest"] != "opaque-server-resume-digest" && vars["digest"] != "explicit-reviewed" {
				t.Error("resume computed digest locally")
			}
		}
		return data("change", Change{Token: "chosen", ContractDigest: "server"})
	})
	value := "x"
	request := Request{Token: "chosen", Name: "n", Kind: "config.set", ConfigSet: ConfigSet{Key: "k", Value: &value, ValueType: "STRING"}, DeviceTokens: []string{"d"}}
	plan, err := a.Plan(t.Context(), request)
	if err != nil || plan.ContractDigest != "server-plan-digest" || plan.Token != "chosen" {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if _, err := a.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "pause", "abort", "resume"} {
		digest := ""
		if action == "start" {
			digest = "reviewed-server-digest"
		}
		if _, err := a.Control(t.Context(), action, "chosen", digest, map[string]any{"maxPostsPerSecond": 1}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	before := len(documents)
	if _, err := a.Control(t.Context(), "start", "chosen", "", nil); err == nil || len(documents) != before {
		t.Fatal("start invented or fetched an approval digest")
	}
	if _, err := a.Control(t.Context(), "resume", "chosen", "explicit-reviewed", nil); err != nil || documents[len(documents)-1] != resumeQuery || len(documents) != before+1 {
		t.Fatal("explicit resume digest did not bypass preview")
	}
}

func TestLinesPagingFiltersAndCSVQuoting(t *testing.T) {
	pages := 0
	token := "dev,\"quoted\"\nnext"
	detail := "error,\"details\"\nmore"
	a := testAPI(t, bulkChangeTestSDL(t), func(query string, vars map[string]any) any {
		if query == statusQuery {
			return data("change", Change{Token: "t"})
		}
		pages++
		criteria := vars["criteria"].(map[string]any)
		if criteria["pageNumber"] != float64(pages) || criteria["posting"] != "POSTED" || criteria["settlement"] != "FAILED" {
			t.Errorf("lost paging/filter: %v", criteria)
		}
		return data("lines", map[string]any{"results": []Line{{Ordinal: pages, TargetToken: &token, Posting: "POSTED", Detail: &detail}}, "pagination": map[string]any{"pageStart": pages, "pageEnd": pages, "totalRecords": 2}})
	})
	lines, err := a.Lines(t.Context(), "t", "POSTED", "FAILED")
	if err != nil || pages != 2 || len(lines) != 2 {
		t.Fatalf("paging: %v pages=%d lines=%v", err, pages, lines)
	}
	var out bytes.Buffer
	if err := PrintLines(&out, lines, "csv"); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][1] != token || rows[1][6] != detail {
		t.Fatalf("CSV lost quoting: %v %v", rows, err)
	}
}

func TestTokenUsesExactFileBytesTenantKindAndIntent(t *testing.T) {
	value := "v"
	intent := ConfigSet{Key: "k", Value: &value, ValueType: "STRING"}
	raw := []byte("d1\r\nd2\r\n")
	canonical, _ := ledger.Canonical(intent)
	want := sha256.Sum256(append(append(append([]byte("tenant"), raw...), []byte("config.set")...), canonical...))
	got, err := DefaultToken("tenant", raw, "config.set", intent)
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("token formula: %q %v", got, err)
	}
	for _, tc := range []struct {
		tenant, kind string
		raw          []byte
		intent       ConfigSet
	}{
		{"other", "config.set", raw, intent}, {"tenant", "other", raw, intent},
		{"tenant", "config.set", []byte("d1\nd2\n"), intent},
		{"tenant", "config.set", raw, ConfigSet{Key: "k", Unset: true}},
	} {
		token, _ := DefaultToken(tc.tenant, tc.raw, tc.kind, tc.intent)
		if token == got {
			t.Fatal("token omitted a component")
		}
	}
	path := filepath.Join(t.TempDir(), "devices.txt")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	devices, bytes, err := ReadDevices(path)
	if err != nil || string(bytes) != string(raw) || strings.Join(devices, ",") != "d1,d2" {
		t.Fatalf("file bytes/order changed: %v %q %v", devices, bytes, err)
	}
}

func TestDevicesFileJoinNeverReconstructsErasedIdentifiers(t *testing.T) {
	lines := []Line{{Ordinal: 1}, {Ordinal: 2, Redacted: true}, {Ordinal: 3}}
	JoinDevices(lines, []string{" first ", "first", "erased", "missing"})
	if textValue(lines[0].InputToken) != "first" || lines[1].InputToken != nil || textValue(lines[2].InputToken) != "missing" {
		t.Fatalf("ordinal provenance/redaction: %+v", lines)
	}
	if lines[0].TargetToken != nil || lines[2].TargetToken != nil {
		t.Fatal("caller provenance was presented as server identity")
	}
}

func TestUnknownFieldNeverRetriesAMutation(t *testing.T) {
	for _, action := range []string{"create", "start", "pause", "resume", "abort"} {
		t.Run(action, func(t *testing.T) {
			mutations, probes := 0, 0
			a := testAPI(t, bulkChangeTestSDL(t), func(_ string, _ map[string]any) any {
				mutations++
				return map[string]any{"errors": []any{map[string]any{"message": "unknown field"}}}
			}, func() string {
				probes++
				return bulkChangeTestSDL(t)
			})
			var err error
			if action == "create" {
				_, err = a.Create(t.Context(), Request{Token: "t", Name: "n", Kind: "config.set", ConfigSet: ConfigSet{Key: "k", Unset: true}, DeviceTokens: []string{"d"}})
			} else {
				_, err = a.Control(t.Context(), action, "t", "reviewed", nil)
			}
			if err == nil || err.Error() != "still upgrading; retry" || mutations != 1 || probes != 1 {
				t.Fatalf("mutation retried or probe skipped: %v mutations=%d probes=%d", err, mutations, probes)
			}
		})
	}
}

func TestResumePreviewRefusalsNeverDispatch(t *testing.T) {
	for _, preview := range []any{
		nil, map[string]any{},
		map[string]any{"contractDigest": "d", "withinContract": false},
		map[string]any{"contractDigest": "d", "withinContract": true, "stepUpRequired": true},
		map[string]any{"contractDigest": "d", "withinContract": true, "thresholdStillExceeded": true},
	} {
		calls := 0
		a := testAPI(t, bulkChangeTestSDL(t), func(query string, _ map[string]any) any {
			calls++
			if query != resumePreviewQuery {
				t.Error("refused preview dispatched a mutation")
			}
			return data("preview", preview)
		})
		if _, err := a.Control(t.Context(), "resume", "t", "", nil); err == nil || calls != 1 {
			t.Fatalf("unsafe preview succeeded: %v calls=%d", err, calls)
		}
	}
}

func TestLinesEmptyOrBrokenPaging(t *testing.T) {
	for _, mode := range []string{"empty", "missing pagination", "no progress", "repeated ordinal"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			a := testAPI(t, bulkChangeTestSDL(t), func(query string, _ map[string]any) any {
				if query == statusQuery {
					return data("change", Change{Token: "t"})
				}
				pages++
				page := map[string]any{"results": []Line{}, "pagination": map[string]any{"pageStart": 1, "pageEnd": 0, "totalRecords": 0}}
				switch mode {
				case "missing pagination":
					delete(page, "pagination")
				case "no progress":
					page["pagination"] = map[string]any{"pageStart": 1, "pageEnd": 0, "totalRecords": 2}
				case "repeated ordinal":
					page["results"] = []Line{{Ordinal: 1}}
					page["pagination"] = map[string]any{"pageStart": 1, "pageEnd": 1, "totalRecords": 2}
				}
				return data("lines", page)
			})
			lines, err := a.Lines(t.Context(), "t", "", "")
			if (err == nil) != (mode == "empty") || mode == "empty" && lines == nil || pages > 2 {
				t.Fatalf("%s: %v lines=%v pages=%d", mode, err, lines, pages)
			}
		})
	}
}

func TestVersionProbeRecognizesBothDirectionsOfARoll(t *testing.T) {
	oldSchema, newSchema := unservedSDL(t), bulkChangeTestSDL(t)
	for _, firstOld := range []bool{true, false} {
		initial, fresh := newSchema, oldSchema
		if firstOld {
			initial, fresh = oldSchema, newSchema
		}
		probes := 0
		a := testAPI(t, initial, func(string, map[string]any) any {
			return map[string]any{"errors": []any{map[string]any{"message": "unknown field bulkChange"}}}
		}, func() string {
			probes++
			return fresh
		})
		if _, err := a.Get(t.Context(), "t"); err == nil || err.Error() != "still upgrading; retry" || probes != 1 {
			t.Fatalf("rolling schema/pod mismatch firstOld=%v: %v probes=%d", firstOld, err, probes)
		}
	}
}

func TestVersionProbeFailureNeverReportsAMissingToken(t *testing.T) {
	a := testAPI(t, bulkChangeTestSDL(t), func(string, map[string]any) any {
		return map[string]any{"errors": []any{map[string]any{"message": "unknown field bulkChange"}}}
	}, func() string { return "not a schema" })
	if _, err := a.Get(t.Context(), "t"); err == nil || !strings.HasPrefix(err.Error(), "still upgrading; retry") || strings.Contains(err.Error(), "not found") {
		t.Fatalf("probe failure became a missing token: %v", err)
	}
}
