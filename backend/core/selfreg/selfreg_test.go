// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selfreg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stub is a platform that answers from a script and records what it was sent.
type stub struct {
	t        *testing.T
	mu       sync.Mutex
	script   []func(w http.ResponseWriter)
	requests []wireRequest
	paths    []string
	// onRequest runs inside the handler before the answer, with the request body parsed.
	onRequest func(n int, r wireRequest)
	srv       *httptest.Server
}

func newStub(t *testing.T, script ...func(w http.ResponseWriter)) *stub {
	s := &stub{t: t, script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var wr wireRequest
		require.NoError(t, json.Unmarshal(raw, &wr))
		s.mu.Lock()
		s.requests = append(s.requests, wr)
		s.paths = append(s.paths, r.URL.EscapedPath())
		n := len(s.requests)
		step := s.script[min(n-1, len(s.script)-1)]
		cb := s.onRequest
		s.mu.Unlock()
		if cb != nil {
			cb(n, wr)
		}
		step(w)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func okBody(outcome string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"outcome":%q,"tenant":"acme","deviceToken":"pv-sn-1-aa","externalId":"SN-1","credentialType":"ACCESS_TOKEN","mqtt":{"clientId":"i:acme:pv-sn-1-aa"}}`, outcome)
	}
}

func refuse(status int, code string, retryAfter string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"code":%q,"message":"m"}`, code)
	}
}

type fakeTime struct {
	sleeps []time.Duration
}

func (f *fakeTime) sleep(ctx context.Context, d time.Duration) error {
	f.sleeps = append(f.sleeps, d)
	return ctx.Err()
}

func opts(t *testing.T, s *stub, ft *fakeTime, rnd float64) Options {
	return Options{BaseURL: s.srv.URL, MaterialFile: filepath.Join(t.TempDir(), "sn-1.material"), AllowPlaintext: true,
		Sleep: ft.sleep, Rand: func() float64 { return rnd }}
}

var req = Request{Tenant: "acme", Key: "fleet", Secret: "s3cret", ExternalId: "SN-1"}

// 🔴 THE MATERIAL IS ON DISK BEFORE THE FIRST BYTE IS SENT. The stub reads the file inside its
// handler, while the request is in flight: if saving came after sending, the file would not
// exist yet, or would hold different material than the body.
func TestTheMaterialIsSavedBeforeItIsSent(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	o := opts(t, s, &fakeTime{}, 0)
	s.onRequest = func(_ int, r wireRequest) {
		onDisk, err := os.ReadFile(o.MaterialFile)
		assert.NoError(t, err, "the request arrived before the material was saved")
		assert.Equal(t, r.CredentialSecret, strings.TrimSpace(string(onDisk)), "the file and the body disagree")
	}

	res, err := Register(context.Background(), req, o)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", res.Outcome)
	assert.True(t, res.MaterialCreated)
	assert.Equal(t, s.requests[0].CredentialSecret, res.Material)
	assert.NoError(t, ValidateMaterial(res.Material))
	assert.Len(t, res.Material, 43)
	assert.Equal(t, "i:acme:pv-sn-1-aa", res.ClientId)
	assert.Equal(t, []string{"/provisioning/acme/register"}, s.paths)
	if runtime.GOOS != "windows" {
		st, err := os.Stat(o.MaterialFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "the credential is world readable")
	}
}

// A second run (a restart, or a retry after a lost answer) finds the material and presents
// the SAME one; it never generates another, which would strand the device's credential.
func TestARerunPresentsTheSameMaterial(t *testing.T) {
	s := newStub(t, okBody("CREATED"), okBody("REPLAYED"))
	o := opts(t, s, &fakeTime{}, 0)
	first, err := Register(context.Background(), req, o)
	require.NoError(t, err)
	second, err := Register(context.Background(), req, o)
	require.NoError(t, err)
	assert.False(t, second.MaterialCreated)
	assert.Equal(t, first.Material, second.Material)
	assert.Equal(t, s.requests[0].CredentialSecret, s.requests[1].CredentialSecret)
	assert.Equal(t, "REPLAYED", second.Outcome)
}

// A file that is not valid material is refused, not replaced: replacing it could discard the
// credential of a device the platform already holds.
func TestAnInvalidMaterialFileIsNeverReplaced(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	o := opts(t, s, &fakeTime{}, 0)
	require.NoError(t, os.WriteFile(o.MaterialFile, []byte("not valid!\n"), 0o600))
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not hold valid credential material")
	assert.Empty(t, s.requests, "something was sent")
	left, _ := os.ReadFile(o.MaterialFile)
	assert.Equal(t, "not valid!\n", string(left))
}

func TestNoMaterialFileIsRefusedBeforeAnySend(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	o := opts(t, s, &fakeTime{}, 0)
	o.MaterialFile = ""
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Empty(t, s.requests)
}

// 429 and 503 wait Retry-After plus a jitter of 0 to 100% of it, and every retry presents the
// same material. The jitter's bounds are exercised at both ends of the generator.
func TestRetryAfterIsHonouredWithJitterAndTheMaterialIsReused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rnd    float64
		status int
		want   time.Duration
	}{
		{"429 no jitter", 0, 429, 7 * time.Second},
		{"429 full jitter", 0.999999, 429, 14 * time.Second},
		{"503 half jitter", 0.5, 503, 10500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, refuse(tc.status, "RATE_LIMITED", "7"), okBody("CREATED"))
			ft := &fakeTime{}
			res, err := Register(context.Background(), req, opts(t, s, ft, tc.rnd))
			require.NoError(t, err)
			require.Len(t, ft.sleeps, 1)
			assert.InDelta(t, float64(tc.want), float64(ft.sleeps[0]), float64(time.Millisecond))
			assert.GreaterOrEqual(t, ft.sleeps[0], 7*time.Second, "waited less than Retry-After")
			assert.LessOrEqual(t, ft.sleeps[0], 14*time.Second, "jitter beyond 100%")
			require.Len(t, s.requests, 2)
			assert.Equal(t, s.requests[0].CredentialSecret, s.requests[1].CredentialSecret)
			assert.Equal(t, 2, res.Attempts)
		})
	}
}

// Without a Retry-After the wait is the exponential backoff with full jitter: from 1 s,
// doubling, capped at 5 min, and anywhere from zero to the step.
func TestBackoffIsExponentialWithFullJitterAndCapped(t *testing.T) {
	assert.Equal(t, time.Second, backoff(0, func() float64 { return 0.999999999 }).Round(time.Second))
	assert.Equal(t, 2*time.Second, backoff(1, func() float64 { return 0.999999999 }).Round(time.Second))
	assert.Equal(t, 4*time.Second, backoff(2, func() float64 { return 0.999999999 }).Round(time.Second))
	assert.Equal(t, 5*time.Minute, backoff(9, func() float64 { return 0.999999999 }).Round(time.Second), "capped at 5 minutes")
	assert.Equal(t, 5*time.Minute, backoff(5000, func() float64 { return 0.999999999 }).Round(time.Second), "an overflowing step is capped")
	assert.Equal(t, time.Duration(0), backoff(3, func() float64 { return 0 }), "full jitter reaches zero")
	assert.Equal(t, 4*time.Second, backoff(3, func() float64 { return 0.5 }))
}

// A refused connection and a 5xx the platform may recover from are retried with backoff and
// the same material.
func TestTransientFailuresAreRetriedWithBackoff(t *testing.T) {
	s := newStub(t, refuse(503, "PROVISIONING_UNAVAILABLE", ""), refuse(500, "PROVISIONING_UNAVAILABLE", ""),
		refuse(502, "", ""), okBody("ADOPTED"))
	ft := &fakeTime{}
	res, err := Register(context.Background(), req, opts(t, s, ft, 0.5))
	require.NoError(t, err)
	assert.Equal(t, "ADOPTED", res.Outcome)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}, ft.sleeps)
	assert.Len(t, s.requests, 4)
}

func TestANetworkErrorIsRetried(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	ft := &fakeTime{}
	o := opts(t, s, ft, 0.5)
	o.HTTPClient = &http.Client{Transport: &flaky{fail: 2, inner: http.DefaultTransport}}
	res, err := Register(context.Background(), req, o)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Attempts)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second}, ft.sleeps)
	assert.Len(t, s.requests, 1, "the failed attempts never reached the server; the material is the one sent")
}

type flaky struct {
	fail  int
	inner http.RoundTripper
}

func (f *flaky) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.fail > 0 {
		f.fail--
		return nil, errors.New("connection reset by peer")
	}
	return f.inner.RoundTrip(r)
}

// The refusals that need a person are NOT retried: one request, the error surfaced, the
// material file left alone. The paired control in each row's script is the retry path above.
func TestRefusalsThatNeedAPersonAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{400, "INVALID_VALUE"}, {401, "PROVISIONING_FAILED"}, {403, "DEVICE_DISABLED"}, {404, "DEVICE_NOT_REGISTERED"},
		{409, "ALREADY_PROVISIONED"}, {409, "DEVICE_NOT_EARMARKED"}, {413, "INVALID_VALUE"}, {422, "LIMIT_EXCEEDED"},
		{500, "PROVISIONING_MISCONFIGURED"}, {501, "CREDENTIAL_TYPE_UNSUPPORTED"},
	} {
		t.Run(fmt.Sprintf("%d %s", tc.status, tc.code), func(t *testing.T) {
			s := newStub(t, refuse(tc.status, tc.code, ""))
			ft := &fakeTime{}
			o := opts(t, s, ft, 0)
			// A bound, so a regression that retries fails at once instead of hanging.
			o.MaxAttempts = 3
			_, err := Register(context.Background(), req, o)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tc.status, apiErr.Status)
			assert.Equal(t, tc.code, apiErr.Code)
			assert.Len(t, s.requests, 1, "a refusal that needs an operator was retried")
			assert.Empty(t, ft.sleeps)
			_, statErr := os.Stat(o.MaterialFile)
			assert.NoError(t, statErr, "the saved material was removed")
		})
	}
}

func TestMaxAttemptsEndsTheRetries(t *testing.T) {
	s := newStub(t, refuse(429, "RATE_LIMITED", "1"))
	o := opts(t, s, &fakeTime{}, 0)
	o.MaxAttempts = 3
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "giving up after 3 attempts")
	var apiErr *APIError
	assert.ErrorAs(t, err, &apiErr)
	assert.Len(t, s.requests, 3)
}

func TestAContextEndsTheWait(t *testing.T) {
	s := newStub(t, refuse(429, "RATE_LIMITED", "1"))
	ctx, cancel := context.WithCancel(context.Background())
	o := opts(t, s, &fakeTime{}, 0)
	o.Sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err := Register(ctx, req, o)
	assert.ErrorIs(t, err, context.Canceled)
}

// A 2xx whose body is not the documented answer is an error, not a registration.
func TestAnUndocumentedSuccessIsAnError(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter) { fmt.Fprint(w, `{"ok":true}`) })
	_, err := Register(context.Background(), req, opts(t, s, &fakeTime{}, 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the documented one")
}

// The tenant is escaped into the path.
func TestTheTenantIsEscaped(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	r := req
	r.Tenant = "a/b c"
	_, err := Register(context.Background(), r, opts(t, s, &fakeTime{}, 0))
	require.NoError(t, err)
	assert.Equal(t, []string{"/provisioning/a%2Fb%20c/register"}, s.paths)
}

func TestValidateMaterial(t *testing.T) {
	assert.NoError(t, ValidateMaterial(strings.Repeat("a", 22)))
	assert.NoError(t, ValidateMaterial(strings.Repeat("a", 256)))
	assert.Error(t, ValidateMaterial(strings.Repeat("a", 21)))
	assert.Error(t, ValidateMaterial(strings.Repeat("a", 257)))
	assert.Error(t, ValidateMaterial(strings.Repeat("a", 21)+"="))
	assert.Error(t, ValidateMaterial(strings.Repeat("a", 21)+"+"))
	m, err := GenerateMaterial()
	require.NoError(t, err)
	assert.NoError(t, ValidateMaterial(m))
	m2, _ := GenerateMaterial()
	assert.NotEqual(t, m, m2)
}

// 🔴 THE PROVISION SECRET NEVER GOES OUT IN PLAINTEXT BY ACCIDENT. An http:// URL is refused
// unless the caller opts in by name, and nothing is saved or sent before the refusal.
func TestPlaintextIsRefusedWithoutAnExplicitOptIn(t *testing.T) {
	s := newStub(t, okBody("CREATED"))
	o := opts(t, s, &fakeTime{}, 0)
	o.AllowPlaintext = false
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext")
	assert.Empty(t, s.requests, "the secret was sent")
	_, statErr := os.Stat(o.MaterialFile)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "material was saved for a request that was never allowed")

	// The control: the same request with the opt-in goes through.
	o.AllowPlaintext = true
	_, err = Register(context.Background(), req, o)
	require.NoError(t, err)

	// Any other scheme is refused outright, opt-in or not.
	o.BaseURL = "ftp://example.com"
	_, err = Register(context.Background(), req, o)
	require.Error(t, err)
}

// 🔴 A REDIRECT IS NEVER FOLLOWED. Go would re-POST the body, the provision secret in it, to the
// Location (an ingress answering 308 to https is the real case). The second server counts what
// reaches it; it must see nothing, and the first must see exactly one request, not retried.
func TestARedirectIsRefusedAndTheBodyIsNeverResent(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			target := newStub(t, okBody("CREATED"))
			from := newStub(t, func(w http.ResponseWriter) {
				w.Header().Set("Location", target.srv.URL+"/provisioning/acme/register")
				w.WriteHeader(status)
			})
			o := opts(t, from, &fakeTime{}, 0)
			o.MaxAttempts = 3
			_, err := Register(context.Background(), req, o)
			var redirect *RedirectError
			require.ErrorAs(t, err, &redirect)
			assert.Equal(t, status, redirect.Status)
			assert.Contains(t, err.Error(), "refusing to follow")
			assert.Len(t, from.requests, 1, "a redirect was retried")
			assert.Empty(t, target.requests, "the secret was re-sent to the redirect target")
		})
	}
}

// A caller's own client gets the same protection: its redirect policy is replaced, not trusted.
func TestACallersClientDoesNotFollowRedirectsEither(t *testing.T) {
	target := newStub(t, okBody("CREATED"))
	from := newStub(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", target.srv.URL)
		w.WriteHeader(308)
	})
	o := opts(t, from, &fakeTime{}, 0)
	o.HTTPClient = &http.Client{} // follows redirects by default
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Empty(t, target.requests)
}

func TestRetryAfterIsCapped(t *testing.T) {
	assert.Equal(t, backoffCeil, parseRetryAfter("86400"))
	assert.Equal(t, 7*time.Second, parseRetryAfter("7"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("-3"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("soon"))
}

func TestAMaterialFileOthersCanReadIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes")
	}
	s := newStub(t, okBody("CREATED"))
	o := opts(t, s, &fakeTime{}, 0)
	m, _ := GenerateMaterial()
	require.NoError(t, os.WriteFile(o.MaterialFile, []byte(m+"\n"), 0o644))
	_, err := Register(context.Background(), req, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "readable by group or others")
	assert.Empty(t, s.requests)
	require.NoError(t, os.Chmod(o.MaterialFile, 0o600))
	_, err = Register(context.Background(), req, o)
	require.NoError(t, err, "the control: the same file at 0600 is accepted")
}

// A dangling symlink at the material path fails; it must not recurse forever.
func TestADanglingSymlinkDoesNotLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "m")
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), link))
	done := make(chan error, 1)
	go func() { _, _, err := LoadOrCreateMaterial(link); done <- err }()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("LoadOrCreateMaterial looped on a dangling symlink")
	}
}
