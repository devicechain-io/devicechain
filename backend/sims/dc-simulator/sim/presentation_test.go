// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// /config.json is the ONLY channel between this process and a presentation client
// that is not this process — web/index.html in a browser, and (FarEndExternal) a Unity
// player holding its own MQTT session per device. Everything below it is wire, and
// until now none of it was gated: presentation.go had no tests at all, so a renamed
// tag, a field wired to the wrong source, or a value dropped by omitempty all shipped
// green and failed in the OTHER process — a scene that renders and receives nothing,
// which is the failure mode this file exists to make impossible to reach silently.

// fakeAuthServer is a user-management data plane that answers the two mutations
// TenantSession drives (login -> selectTenant). It hands out a distinctive access
// token so a test can tell "the handler served THE session's token" from "the handler
// served some string".
type fakeAuthServer struct {
	accessToken string
	// fail makes every exchange a GraphQL error, which is how a real one fails when
	// the sim identity's password was rotated or user-management is down.
	fail bool
}

func (f *fakeAuthServer) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.fail {
		writeJSON(w, http.StatusOK, map[string]any{
			"errors": []map[string]any{{"message": "fake: identity rejected"}},
		})
		return
	}
	switch {
	case strings.Contains(req.Query, "login("):
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"login": map[string]any{"identityToken": "identity-tok", "expiresAt": "", "superuser": false},
		}})
	case strings.Contains(req.Query, "selectTenant("):
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"selectTenant": map[string]any{
				"accessToken": f.accessToken, "refreshToken": "refresh-tok", "expiresAt": "",
			},
		}})
	default:
		http.Error(w, "fake: unexpected query "+req.Query, http.StatusBadRequest)
	}
}

// presentationFixture builds a Runtime whose every presentation-visible value is
// DISTINCTIVE, so a field wired to the wrong source (or to nothing) reads as a
// mismatch rather than coinciding with whatever else the struct carries. mutate runs
// last so a case can vary one field without restating the rest.
func presentationFixture(t *testing.T, auth *fakeAuthServer, mutate func(*Runtime)) (*http.ServeMux, *Runtime) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(auth.serve))
	t.Cleanup(srv.Close)

	rt := &Runtime{
		Endpoints:       Endpoints{EventMgmtWS: "wss://events.example.invalid/graphql"},
		InstanceId:      "instance-under-test",
		Tenant:          "tenant-under-test",
		MqttBroker:      "ssl://broker.example.invalid:1883",
		MqttTLSInsecure: true,
		Session: userclient.NewTenantSession(srv.Client(), srv.URL,
			"sim@example.invalid", "pw", "tenant-under-test"),
	}
	if mutate != nil {
		mutate(rt)
	}

	mux := http.NewServeMux()
	// A non-empty web root: http.FileServerFS panics on a nil FS, and the page itself
	// is not what is under test here.
	RegisterPresentation(mux, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}},
		rt, "manifest-under-test")
	return mux, rt
}

// fetchConfig drives the real handler and returns the status plus the RAW body, so a
// caller decides how to read it. Nothing here decodes into presentationConfig — see
// TestPresentationConfigWireNamesAreTheContract for why that would forgive the
// breakages that matter.
func fetchConfig(t *testing.T, mux *http.ServeMux) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config.json", nil))
	return rec.Code, rec.Body.Bytes()
}

// configMap does what a real reader of /config.json does: it takes the response bytes
// and looks up KEYS, with no Go type in between to quietly translate them.
func configMap(t *testing.T, mux *http.ServeMux) map[string]any {
	t.Helper()
	code, body := fetchConfig(t, mux)
	if code != http.StatusOK {
		t.Fatalf("/config.json returned %d: %s", code, body)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode /config.json: %v (body %s)", err, body)
	}
	return cfg
}

func configKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 🔴 THE WIRE NAMES ARE THE CONTRACT. The consumers of /config.json are web/index.html
// (plain JS reading cfg.wsUrl, cfg.token) and an out-of-process presentation client;
// neither can see a Go field name, and a renamed or deleted tag compiles cleanly.
//
// It decodes into a map rather than a struct with tags DELIBERATELY, and this is not
// stylistic caution — it is a defect that was found in the sibling change. Go's
// encoding/json matches keys case-insensitively and falls back to the Go FIELD NAME
// when a tag is absent, so a test decoding into a struct tagged `json:"mqttBroker"`
// still reads a body that emits "MqttBroker". Deleting a tag outright — the single
// likeliest way this breaks, since it is what a careless refactor of the struct does —
// would leave such a test green while every non-Go client got a key it does not know.
//
// 🔴 It asserts the key set EXACTLY, and that half is not symmetry for its own sake —
// it is the only gate on the largest comment in presentation.go. That comment spends
// four numbered reasons on why device credentials must never appear in this body, and
// an argument in a comment stops nothing: a per-key presence loop passes for any ADDED
// field, so someone "fixing" the omission by serving a device secret ships it with
// every test green. Demonstrated, not hypothesised — adding a `credentialValue` field
// and populating it left all five tests in this file passing.
func TestPresentationConfigWireNamesAreTheContract(t *testing.T) {
	mux, _ := presentationFixture(t, &fakeAuthServer{accessToken: "access-tok"}, nil)
	cfg := configMap(t, mux)

	// TLS-insecure is true in the fixture on purpose: this case is about the KEYS
	// existing, and asserting a false bool here would make it fail for the unrelated
	// reason that omitempty dropped it. That case has its own test below.
	want := map[string]any{
		"tenant":          "tenant-under-test",
		"manifestId":      "manifest-under-test",
		"wsUrl":           "wss://events.example.invalid/graphql",
		"token":           "access-tok",
		"instanceId":      "instance-under-test",
		"mqttBroker":      "ssl://broker.example.invalid:1883",
		"mqttTLSInsecure": true,
	}
	for key, wantVal := range want {
		got, ok := cfg[key]
		if !ok {
			t.Errorf("/config.json carries no %q key; it has %v", key, configKeys(cfg))
			continue
		}
		if got != wantVal {
			t.Errorf("/config.json %q is %#v, want %#v", key, got, wantVal)
		}
	}

	// The closed set. A key nobody expected is not a harmless addition here: this body
	// is served with NO authentication at all, to whatever can reach the bind address,
	// so every field in it is a disclosure decision — and the one this endpoint most
	// specifically must not make is a device credential, which unlike the token beside
	// it is permanent and non-rotating. Adding a field is allowed; adding it without
	// coming through this list, and through the four numbered reasons on
	// presentationConfig, is not.
	for _, key := range configKeys(cfg) {
		if _, expected := want[key]; !expected {
			t.Errorf("/config.json carries an unexpected key %q (value %#v). This body is "+
				"unauthenticated, so a new field is a new disclosure: if it is intended, add it "+
				"to this list — and if it is device credential material, see the four reasons on "+
				"presentationConfig for why it must not be served here at all", key, cfg[key])
		}
	}
}

// The three MQTT-identity fields are the ones a FarEndExternal client cannot work
// around, and each has exactly one correct source on Runtime. This asserts they are
// THREADED rather than hardcoded or left zero — the mode of failure being that the
// client dials a broker it was told nothing about, or builds a client id the gateway's
// auth callout refuses because the instance segment is empty.
//
// The values are deliberately unlike the fixture's defaults so a handler that read
// rt.Endpoints.MqttBroker, or a constant, cannot coincide with the expected answer.
func TestPresentationConfigThreadsMqttIdentityFromTheRuntime(t *testing.T) {
	mux, _ := presentationFixture(t, &fakeAuthServer{accessToken: "access-tok"}, func(rt *Runtime) {
		rt.InstanceId = "threaded-instance"
		rt.MqttBroker = "tcp://threaded-broker.example.invalid:1883"
		// Endpoints carries a DIFFERENT broker. The flattened Runtime field is what the
		// far end dials (attachCommandFarEnd reads rt.MqttBroker), so serving the nested
		// copy would be a second source that can disagree with the one that matters —
		// and with both set to the same string, that bug would pass.
		rt.Endpoints.MqttBroker = "tcp://wrong-broker.example.invalid:1883"
	})
	cfg := configMap(t, mux)

	if got := cfg["instanceId"]; got != "threaded-instance" {
		t.Errorf("instanceId is %#v, want the Runtime's %q — without it a client cannot build "+
			"the {instance}:{tenant}:{deviceToken} client id the broker's auth callout accepts",
			got, "threaded-instance")
	}
	if got := cfg["mqttBroker"]; got != "tcp://threaded-broker.example.invalid:1883" {
		t.Errorf("mqttBroker is %#v, want the Runtime's flattened MqttBroker %q — the same value "+
			"/status reports as commandFarEnd.broker, and the one attachCommandFarEnd gates on",
			got, "tcp://threaded-broker.example.invalid:1883")
	}
}

// 🔴 A trust decision must not be reported by ABSENCE. `mqttTLSInsecure` is a bool, and
// a bool tagged omitempty disappears from the body when it is false — leaving a client
// unable to tell "verify the gateway's certificate" from "this build does not send the
// field", and the two readings differ by whether the connection is authenticated at
// all. The dangerous direction is exactly the one omitempty takes, so the false case
// asserts PRESENCE first and value second.
func TestPresentationConfigKeepsFalseTLSInsecureOnTheWire(t *testing.T) {
	mux, _ := presentationFixture(t, &fakeAuthServer{accessToken: "access-tok"}, func(rt *Runtime) {
		rt.MqttTLSInsecure = false
	})
	cfg := configMap(t, mux)

	got, ok := cfg["mqttTLSInsecure"]
	if !ok {
		t.Fatalf("/config.json omits %q when it is false, so a client reads an absent key rather "+
			"than a verification decision; it has %v", "mqttTLSInsecure", configKeys(cfg))
	}
	if got != false {
		t.Errorf("/config.json %q is %#v for a verifying handshake, want false", "mqttTLSInsecure", got)
	}
}

// The token is resolved per REQUEST (a page opened an hour after start must not get an
// expired one), so its acquisition can fail long after the process came up healthy.
// The only safe answer then is to refuse the whole config: a 200 carrying an empty
// token is a body the client BELIEVES, and it fails later as a subscribe the platform
// rejects with no trace of the cause — while the fields around it are all correct,
// which is what makes it look like a platform problem rather than an auth one.
func TestPresentationConfigRefusesToServeATokenlessConfig(t *testing.T) {
	mux, _ := presentationFixture(t, &fakeAuthServer{fail: true}, nil)
	code, body := fetchConfig(t, mux)

	if code == http.StatusOK {
		t.Fatalf("/config.json returned 200 when the session could not mint a token; body %s", body)
	}
	if code != http.StatusBadGateway {
		t.Errorf("/config.json returned %d, want %d", code, http.StatusBadGateway)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode error body: %v (body %s)", err, body)
	}
	if _, ok := decoded["token"]; ok {
		t.Errorf("the failure body still carries a %q key, so a client cannot tell it apart from "+
			"a config", "token")
	}
	if msg, _ := decoded["error"].(string); msg == "" {
		t.Errorf("the failure body names no cause; it has %v", configKeys(decoded))
	}
}

// A successful config must carry the token the SESSION holds, not a placeholder. This
// is the counterweight to the refusal above: a handler that returned "" and a 200 would
// pass every key-presence assertion in this file.
func TestPresentationConfigServesTheSessionsToken(t *testing.T) {
	mux, _ := presentationFixture(t, &fakeAuthServer{accessToken: "distinctive-access-token"}, nil)
	cfg := configMap(t, mux)

	if got := cfg["token"]; got != "distinctive-access-token" {
		t.Errorf("/config.json token is %#v, want the session's %q",
			got, "distinctive-access-token")
	}
}
