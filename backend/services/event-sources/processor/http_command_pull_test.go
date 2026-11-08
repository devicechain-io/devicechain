// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePullBackend is a CommandPullBackend with scripted answers and a record of what it was asked.
type fakePullBackend struct {
	mu       sync.Mutex
	tokens   map[string]*PullDevice // access token -> device
	authErr  error
	queue    []PulledCommand // handed out once, then gone
	pullErr  error
	pulls    int
	ackAns   PullAnswer
	respAns  PullAnswer
	opErr    error
	acked    []string
	answered []string
	gotBody  struct {
		success bool
		payload *string
		errMsg  *string
	}
	authTenant string
}

func (f *fakePullBackend) Authenticate(_ context.Context, tenant, token string) (*PullDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authTenant = tenant
	if f.authErr != nil {
		return nil, f.authErr
	}
	return f.tokens[token], nil
}

func (f *fakePullBackend) Pull(_ context.Context, _, _ string, limit int) ([]PulledCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	out := f.queue
	f.queue = nil
	return out, nil
}

func (f *fakePullBackend) Ack(_ context.Context, _, _, command string) (PullAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, command)
	return f.ackAns, f.opErr
}

func (f *fakePullBackend) Respond(_ context.Context, _, _, command string, success bool, payload, errMsg *string) (PullAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, command)
	f.gotBody.success, f.gotBody.payload, f.gotBody.errMsg = success, payload, errMsg
	return f.respAns, f.opErr
}

type pullHarness struct {
	es      *HttpEventSource
	rt      *CommandPullRoute
	backend *fakePullBackend
	srv     *httptest.Server
	results map[string]int
	mu      sync.Mutex
}

func newPullHarness(t *testing.T, mutate func(*CommandPullRoute)) *pullHarness {
	t.Helper()
	es, _, _ := newTestHttpSource(t, nil)
	h := &pullHarness{es: es, results: map[string]int{}}
	h.backend = &fakePullBackend{
		tokens: map[string]*PullDevice{
			"tok-d1":    {Token: "d1", OptedIn: true},
			"tok-d2":    {Token: "d2", OptedIn: true},
			"tok-plain": {Token: "plain", OptedIn: false},
		},
		ackAns:  AnswerRecorded,
		respAns: AnswerRecorded,
	}
	h.rt = &CommandPullRoute{
		Backend:             h.backend,
		Sources:             sourcelimit.Must(sourcelimit.Limits{Burst: 1000, Every: time.Millisecond, MaxKeys: 16}),
		MaxPollersPerTenant: 2,
		PollInterval:        20 * time.Millisecond,
		Metrics: CommandPullMetrics{Request: func(op, result string) {
			h.mu.Lock()
			h.results[op+"/"+result]++
			h.mu.Unlock()
		}},
	}
	if mutate != nil {
		mutate(h.rt)
	}
	require.NoError(t, es.UseCommandPull(h.rt))
	h.rt.begin()
	h.srv = httptest.NewServer(es.handler())
	t.Cleanup(func() { h.rt.end(); h.srv.Close() })
	return h
}

func (h *pullHarness) do(t *testing.T, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *pullHarness) count(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.results[key]
}

const pullPath = "/inst-1/acme/devices/d1/commands"

func TestPollReturnsCommandsAsJSON(t *testing.T) {
	h := newPullHarness(t, nil)
	payload, mode, exp := `{"on":true}`, "ONEWAY", "2030-01-01T00:00:00Z"
	h.backend.queue = []PulledCommand{{Token: "c1", Name: "relay", Payload: &payload, DeliveryMode: &mode, Attempt: 1, ExpiresAt: &exp}}

	resp := h.do(t, http.MethodGet, pullPath+"?wait=0", "tok-d1", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var got struct {
		Commands []struct {
			Token        string          `json:"token"`
			Name         string          `json:"name"`
			Payload      json.RawMessage `json:"payload"`
			DeliveryMode string          `json:"deliveryMode"`
			Attempt      int             `json:"attempt"`
			ExpiresAt    string          `json:"expiresAt"`
		} `json:"commands"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got.Commands, 1)
	c := got.Commands[0]
	assert.Equal(t, "c1", c.Token)
	assert.Equal(t, "relay", c.Name)
	assert.JSONEq(t, `{"on":true}`, string(c.Payload))
	assert.Equal(t, "ONEWAY", c.DeliveryMode)
	assert.Equal(t, 1, c.Attempt)
	assert.Equal(t, exp, c.ExpiresAt)
	assert.Equal(t, "acme", h.backend.authTenant)
	assert.Equal(t, 1, h.count("poll/commands"))
}

func TestPollWithNothingWaitsThenAnswers204(t *testing.T) {
	h := newPullHarness(t, nil)
	start := time.Now()
	resp := h.do(t, http.MethodGet, pullPath+"?wait=1", "tok-d1", "")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond, "the poll must hold for its wait")
	h.backend.mu.Lock()
	pulls := h.backend.pulls
	h.backend.mu.Unlock()
	assert.Greater(t, pulls, 2, "a held poll asks the platform repeatedly")
}

func TestPollWithWaitZeroReturnsAtOnce(t *testing.T) {
	h := newPullHarness(t, nil)
	start := time.Now()
	resp := h.do(t, http.MethodGet, pullPath+"?wait=0", "tok-d1", "")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	assert.Equal(t, 1, h.count("poll/empty"))
}

func TestPollIsReleasedWhenACommandArrivesWhileWaiting(t *testing.T) {
	h := newPullHarness(t, nil)
	go func() {
		time.Sleep(150 * time.Millisecond)
		h.backend.mu.Lock()
		h.backend.queue = []PulledCommand{{Token: "late", Name: "x", Attempt: 1}}
		h.backend.mu.Unlock()
	}()
	start := time.Now()
	resp := h.do(t, http.MethodGet, pullPath+"?wait=20", "tok-d1", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Less(t, time.Since(start), 5*time.Second, "a command must release the poll, not the deadline")
}

func TestPollRejectsABadWait(t *testing.T) {
	h := newPullHarness(t, nil)
	for _, w := range []string{"abc", "-1", "1.5"} {
		resp := h.do(t, http.MethodGet, pullPath+"?wait="+w, "tok-d1", "")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "wait=%s", w)
	}
}

func TestPollClampsAWaitOverTheMaximum(t *testing.T) {
	// A wait of 31 is accepted and clamped, not refused; the held request is cut short here by
	// stopping the route, which also proves a stop releases the pollers.
	h := newPullHarness(t, nil)
	done := make(chan int, 1)
	go func() {
		resp := h.do(t, http.MethodGet, pullPath+"?wait=31", "tok-d1", "")
		done <- resp.StatusCode
	}()
	time.Sleep(200 * time.Millisecond)
	h.rt.end()
	select {
	case code := <-done:
		assert.Equal(t, http.StatusNoContent, code)
	case <-time.After(3 * time.Second):
		t.Fatal("stopping the route did not release a held poll")
	}
}

func TestPollerCapPerTenantIsEnforced(t *testing.T) {
	h := newPullHarness(t, nil) // cap 2
	var wg sync.WaitGroup
	held := func(path, token string) {
		defer wg.Done()
		h.do(t, http.MethodGet, path+"?wait=2", token, "")
	}
	wg.Add(2)
	go held(pullPath, "tok-d1")
	go held("/inst-1/acme/devices/d2/commands", "tok-d2")
	require.Eventually(t, func() bool {
		h.rt.mu.Lock()
		defer h.rt.mu.Unlock()
		return h.rt.pollers["acme"] == 2
	}, 2*time.Second, 10*time.Millisecond)

	over := h.do(t, http.MethodGet, pullPath+"?wait=2", "tok-d1", "")
	assert.Equal(t, http.StatusTooManyRequests, over.StatusCode)
	assert.NotEmpty(t, over.Header.Get("Retry-After"))
	assert.Equal(t, 1, h.count("poll/poller_cap"))

	// A wait=0 request is never held and is not counted against the cap.
	instant := h.do(t, http.MethodGet, pullPath+"?wait=0", "tok-d1", "")
	assert.Equal(t, http.StatusNoContent, instant.StatusCode)

	// Another tenant has its own allowance.
	other := h.do(t, http.MethodGet, "/inst-1/globex/devices/d1/commands?wait=0", "tok-d1", "")
	assert.Equal(t, http.StatusNoContent, other.StatusCode)
	wg.Wait()

	h.rt.mu.Lock()
	defer h.rt.mu.Unlock()
	assert.Empty(t, h.rt.pollers, "every held poll must release its slot")
}

func TestAuthenticationRefusals(t *testing.T) {
	h := newPullHarness(t, nil)
	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"no header", http.MethodGet, pullPath + "?wait=0", "", http.StatusUnauthorized},
		{"unknown token", http.MethodGet, pullPath + "?wait=0", "nope", http.StatusUnauthorized},
		{"another device's token", http.MethodGet, pullPath + "?wait=0", "tok-d2", http.StatusForbidden},
		{"type has not opted in", http.MethodGet, "/inst-1/acme/devices/plain/commands?wait=0", "tok-plain", http.StatusForbidden},
		{"ack with no header", http.MethodPost, pullPath + "/c1/ack", "", http.StatusUnauthorized},
		{"ack for another device", http.MethodPost, pullPath + "/c1/ack", "tok-d2", http.StatusForbidden},
		{"response with an unknown token", http.MethodPost, pullPath + "/c1/response", "nope", http.StatusUnauthorized},
		{"invalid tenant", http.MethodGet, "/inst-1/BAD%20TENANT/devices/d1/commands?wait=0", "tok-d1", http.StatusBadRequest},
	}
	for _, c := range cases {
		resp := h.do(t, c.method, c.path, c.token, `{"success":true}`)
		assert.Equal(t, c.want, resp.StatusCode, c.name)
	}
	assert.Zero(t, h.backend.pulls, "a refused request must not reach command-delivery")
	assert.Empty(t, h.backend.acked)
	assert.Empty(t, h.backend.answered)
	assert.Equal(t, "Bearer", func() string {
		return h.do(t, http.MethodGet, pullPath+"?wait=0", "", "").Header.Get("WWW-Authenticate")
	}())
}

func TestATokenInTheURLIsNotAccepted(t *testing.T) {
	h := newPullHarness(t, nil)
	resp := h.do(t, http.MethodGet, pullPath+"?wait=0&access_token=tok-d1", "", "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthenticationBackendFailureIs503NotARefusal(t *testing.T) {
	h := newPullHarness(t, nil)
	h.backend.authErr = errors.New("device-management down")
	resp := h.do(t, http.MethodGet, pullPath+"?wait=0", "tok-d1", "")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, 1, h.count("poll/unavailable"))
}

func TestPullBackendFailureIs503(t *testing.T) {
	h := newPullHarness(t, nil)
	h.backend.pullErr = errors.New("command-delivery down")
	resp := h.do(t, http.MethodGet, pullPath+"?wait=0", "tok-d1", "")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestSourceLimitBoundsGuessing(t *testing.T) {
	h := newPullHarness(t, func(rt *CommandPullRoute) {
		rt.Sources = sourcelimit.Must(sourcelimit.Limits{Burst: 3, Every: time.Hour, MaxKeys: 4})
	})
	codes := []int{}
	for i := 0; i < 5; i++ {
		codes = append(codes, h.do(t, http.MethodGet, pullPath+"?wait=0", "guess", "").StatusCode)
	}
	assert.Equal(t, []int{401, 401, 401, 429, 429}, codes)
	assert.NotEmpty(t, h.do(t, http.MethodGet, pullPath+"?wait=0", "guess", "").Header.Get("Retry-After"))
	h.backend.mu.Lock()
	assert.Zero(t, h.backend.pulls, "no guess reached command-delivery")
	h.backend.mu.Unlock()
}

func TestAckAnswers(t *testing.T) {
	cases := []struct {
		ans  PullAnswer
		want int
	}{
		{AnswerRecorded, http.StatusNoContent},
		{AnswerNotFound, http.StatusNotFound},
		{AnswerNotPulled, http.StatusConflict},
		{AnswerNotAwaiting, http.StatusConflict},
		{PullAnswer("SOMETHING_NEW"), http.StatusBadGateway},
	}
	for _, c := range cases {
		h := newPullHarness(t, nil)
		h.backend.ackAns = c.ans
		resp := h.do(t, http.MethodPost, pullPath+"/c1/ack", "tok-d1", "")
		assert.Equal(t, c.want, resp.StatusCode, string(c.ans))
		assert.Equal(t, []string{"c1"}, h.backend.acked)
	}
}

func TestAckBackendFailureIs503SoTheDeviceRetries(t *testing.T) {
	h := newPullHarness(t, nil)
	h.backend.opErr = errors.New("db down")
	resp := h.do(t, http.MethodPost, pullPath+"/c1/ack", "tok-d1", "")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestResponseBodyIsParsedAndForwarded(t *testing.T) {
	cases := []struct {
		name, body  string
		wantSuccess bool
		wantPayload *string
		wantErr     *string
	}{
		{"object payload carried as JSON text", `{"success":true,"payload":{"temp":21}}`, true, ptrS(`{"temp":21}`), nil},
		{"string payload is the text itself", `{"success":true,"payload":"done"}`, true, ptrS("done"), nil},
		{"null payload is none", `{"success":true,"payload":null}`, true, nil, nil},
		{"failure with a reason", `{"success":false,"error":"valve stuck"}`, false, nil, ptrS("valve stuck")},
	}
	for _, c := range cases {
		h := newPullHarness(t, nil)
		resp := h.do(t, http.MethodPost, pullPath+"/c1/response", "tok-d1", c.body)
		require.Equal(t, http.StatusNoContent, resp.StatusCode, c.name)
		assert.Equal(t, c.wantSuccess, h.backend.gotBody.success, c.name)
		assert.Equal(t, c.wantPayload, h.backend.gotBody.payload, c.name)
		assert.Equal(t, c.wantErr, h.backend.gotBody.errMsg, c.name)
	}
}

func ptrS(s string) *string { return &s }

func TestResponseRejectsMalformedBodies(t *testing.T) {
	h := newPullHarness(t, nil)
	for _, body := range []string{``, `not json`, `{}`, `{"payload":1}`, `{"success":"yes"}`} {
		resp := h.do(t, http.MethodPost, pullPath+"/c1/response", "tok-d1", body)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "body %q", body)
	}
	assert.Empty(t, h.backend.answered, "a malformed response must not reach command-delivery")
}

func TestResponseBodyIsCapped(t *testing.T) {
	h := newPullHarness(t, nil)
	big := `{"success":true,"payload":"` + strings.Repeat("a", maxPullResponseBytes) + `"}`
	resp := h.do(t, http.MethodPost, pullPath+"/c1/response", "tok-d1", big)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, h.backend.answered)
}

func TestResponseAnswers(t *testing.T) {
	cases := map[PullAnswer]int{
		AnswerRecorded:    http.StatusNoContent,
		AnswerNotFound:    http.StatusNotFound,
		AnswerNotPulled:   http.StatusConflict,
		AnswerNotAnswered: http.StatusConflict,
	}
	for ans, want := range cases {
		h := newPullHarness(t, nil)
		h.backend.respAns = ans
		resp := h.do(t, http.MethodPost, pullPath+"/c1/response", "tok-d1", `{"success":true}`)
		assert.Equal(t, want, resp.StatusCode, string(ans))
	}
}

func TestRoutesAnswer503WhenNotConfigured(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)
	srv := httptest.NewServer(es.handler())
	defer srv.Close()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, pullPath},
		{http.MethodPost, pullPath + "/c1/ack"},
		{http.MethodPost, pullPath + "/c1/response"},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		req.Header.Set("Authorization", "Bearer x")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, c.method+" "+c.path)
	}
}

func TestOnlyTheDocumentedMethodsAreServed(t *testing.T) {
	h := newPullHarness(t, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, pullPath},
		{http.MethodGet, pullPath + "/c1/ack"},
		{http.MethodGet, pullPath + "/c1/response"},
		{http.MethodDelete, pullPath},
	} {
		resp := h.do(t, c.method, c.path, "tok-d1", "")
		assert.Contains(t, []int{http.StatusMethodNotAllowed, http.StatusNotFound}, resp.StatusCode, c.method+" "+c.path)
	}
}

func TestEventsRouteIsUnchanged(t *testing.T) {
	// The pull routes must not shadow device ingest.
	h := newPullHarness(t, nil)
	resp := h.do(t, http.MethodPost, "/inst-1/acme/events", "", `{"type":"Measurement"}`)
	assert.NotEqual(t, http.StatusNotFound, resp.StatusCode)
	assert.NotEqual(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestValidate(t *testing.T) {
	build := func() *CommandPullRoute {
		return &CommandPullRoute{Backend: &fakePullBackend{},
			Sources:             sourcelimit.Must(sourcelimit.Limits{Burst: 1, Every: time.Second, MaxKeys: 1}),
			MaxPollersPerTenant: 1}
	}
	require.NoError(t, build().Validate())
	for name, mutate := range map[string]func(*CommandPullRoute){
		"no backend": func(r *CommandPullRoute) { r.Backend = nil },
		"no sources": func(r *CommandPullRoute) { r.Sources = nil },
		"no cap":     func(r *CommandPullRoute) { r.MaxPollersPerTenant = 0 },
	} {
		r := build()
		mutate(r)
		assert.Error(t, r.Validate(), name)
	}
	var nilRoute *CommandPullRoute
	assert.Error(t, nilRoute.Validate())
}

// A held poll proves the device on every wake, so a credential revoked, a device disabled or a type
// moved off HTTP_PULL mid-poll stops the next claim rather than the next request.
func TestAHeldPollStopsWhenTheCredentialIsRevokedMidPoll(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(f *fakePullBackend)
		status int
	}{
		"revoked": {func(f *fakePullBackend) { delete(f.tokens, "tok-d1") }, http.StatusUnauthorized},
		"opted out": {func(f *fakePullBackend) { f.tokens["tok-d1"] = &PullDevice{Token: "d1", OptedIn: false} },
			http.StatusForbidden},
		"auth down": {func(f *fakePullBackend) { f.authErr = errors.New("device-management down") },
			http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPullHarness(t, nil)
			done := make(chan *http.Response, 1)
			go func() { done <- h.do(t, http.MethodGet, pullPath+"?wait=10", "tok-d1", "") }()
			require.Eventually(t, func() bool {
				h.backend.mu.Lock()
				defer h.backend.mu.Unlock()
				return h.backend.pulls >= 1
			}, 2*time.Second, 5*time.Millisecond)

			h.backend.mu.Lock()
			tc.change(h.backend)
			h.backend.queue = []PulledCommand{{Token: "late", Name: "reboot", Attempt: 1}}
			pullsAtChange := h.backend.pulls
			h.backend.mu.Unlock()

			select {
			case resp := <-done:
				assert.Equal(t, tc.status, resp.StatusCode)
			case <-time.After(3 * time.Second):
				t.Fatal("the held poll outlived the change")
			}
			h.backend.mu.Lock()
			defer h.backend.mu.Unlock()
			assert.LessOrEqual(t, h.backend.pulls, pullsAtChange+0, "no claim may follow the refusal")
			assert.Len(t, h.backend.queue, 1, "the command must not have been claimed")
		})
	}
}

// The counterweight: re-proving on each wake does not disturb a poll that stays valid.
func TestAHeldPollStillDeliversAfterSeveralReauthentications(t *testing.T) {
	h := newPullHarness(t, nil)
	done := make(chan *http.Response, 1)
	go func() { done <- h.do(t, http.MethodGet, pullPath+"?wait=10", "tok-d1", "") }()
	time.Sleep(150 * time.Millisecond)
	h.backend.mu.Lock()
	h.backend.queue = []PulledCommand{{Token: "c1", Name: "reboot", Attempt: 1}}
	h.backend.mu.Unlock()
	select {
	case resp := <-done:
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	case <-time.After(3 * time.Second):
		t.Fatal("poll never delivered")
	}
}
