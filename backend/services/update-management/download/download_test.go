// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
)

// countingStore wraps a real store and counts how artifact bytes were opened, and by how large a
// read, so a test can say "nothing was opened" for a refusal and "never more than a chunk at a
// time" for a download.
type countingStore struct {
	blob.Store
	opens   atomic.Int64
	maxRead atomic.Int64
	// gate, when set, makes the returned reader block until it is closed (a slow client).
	gate    chan struct{}
	started chan struct{}
	// readDelay, when set, makes every Read of the returned reader take at least that long: a
	// source slow enough that the elapsed time, not the speed of the socket, decides the outcome.
	readDelay atomic.Int64 // a time.Duration; atomic because the handler goroutine outlives the client's view
}

type countingReader struct {
	io.ReadCloser
	s *countingStore
}

func (r *countingReader) Read(p []byte) (int, error) {
	if int64(len(p)) > r.s.maxRead.Load() {
		r.s.maxRead.Store(int64(len(p)))
	}
	if d := time.Duration(r.s.readDelay.Load()); d > 0 {
		time.Sleep(d) // never returns early, so d has provably elapsed afterwards
	}
	return r.ReadCloser.Read(p)
}

func (s *countingStore) OpenRange(ctx context.Context, ref blob.Ref, off, n int64) (io.ReadCloser, blob.Info, error) {
	s.opens.Add(1)
	rc, info, err := s.Store.OpenRange(ctx, ref, off, n)
	if err != nil {
		return nil, info, err
	}
	if s.gate != nil {
		close(s.started)
		<-s.gate
	}
	return &countingReader{ReadCloser: rc, s: s}, info, nil
}

func (s *countingStore) Open(ctx context.Context, ref blob.Ref) (io.ReadCloser, blob.Info, error) {
	s.opens.Add(1)
	return s.Store.Open(ctx, ref)
}

type env struct {
	api     *model.Api
	store   *countingStore
	h       *Handler
	srv     *httptest.Server
	clock   time.Time
	profile map[string]string
	devices *fakeDevices
}

type fakeDevices struct {
	profiles map[string]string
	// disabled is the set of tenant/device keys an operator has disabled; a test flips it mid-run.
	disabled map[string]bool
}

func (f *fakeDevices) DeviceOf(_ context.Context, tenant, device string) (model.DeviceFacts, bool, error) {
	p, ok := f.profiles[tenant+"/"+device]
	return model.DeviceFacts{Profile: p, Disabled: f.disabled[tenant+"/"+device]}, ok, nil
}

type nullDesired struct{}

func (nullDesired) PublishDesired(context.Context, string, []byte) error { return nil }

type profiles struct{}

func (profiles) Missing(context.Context, string, []string) ([]string, error) { return nil, nil }

func ctxOf(tenant string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), &auth.Claims{Tenant: tenant, Username: "alice"})
}

func newEnv(t *testing.T, maxConcurrent int) *env {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	for _, m := range schema.Migrations {
		require.NoError(t, m.Migrate(db))
	}
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	real, err := blob.New(context.Background(), blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)
	store := &countingStore{Store: real}
	kp, err := secrets.NewInstanceKeyProvider(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)

	e := &env{store: store, clock: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	api := model.NewApi(&rdb.RdbManager{Database: db}, store, model.Limits{
		MaxArtifactBytes: 64 << 20, ConfiguredMaxArtifactBytes: 64 << 20, MaxTenantArtifactBytes: 256 << 20,
	})
	api.Profiles = profiles{}
	e.devices = &fakeDevices{profiles: map[string]string{"acme/dev-1": "gateway", "acme/dev-2": "gateway", "globex/dev-1": "gateway"},
		disabled: map[string]bool{}}
	api.Devices = e.devices
	api.Desired = nullDesired{}
	api.Secrets = secrets.NewStore(db, kp)
	api.Policy = ota.Policy{Acknowledge: time.Hour, Download: time.Hour, Verify: time.Hour, Install: time.Hour, Confirm: time.Hour}
	api.AllowUnverified = true
	api.DownloadBaseURL = "https://updates.example.test/api/update-management"
	api.CapabilityTTL = 10 * time.Minute
	api.Clock = func() time.Time { return e.clock }
	e.api = api
	e.h = New(api, maxConcurrent, nil)
	mux := http.NewServeMux()
	mux.Handle(Path, e.h)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) artifact(t *testing.T, tenant, token string, content []byte) *model.Artifact {
	t.Helper()
	ctx := ctxOf(tenant)
	sum := sha256.Sum256(content)
	a, err := e.api.CreateArtifact(ctx, &model.ArtifactCreateRequest{
		Token: token, Kind: model.KindFirmware, Name: "fw", Version: token, Component: "firmware",
		SizeBytes: int64(len(content)), Digest: model.DigestPrefix + hex.EncodeToString(sum[:]),
		CompatibleProfiles: []string{"gateway"},
	})
	require.NoError(t, err)
	ref, err := e.store.Put(ctx, blob.Key{Tenant: tenant, Purpose: model.BlobPurpose, ID: token + "-object"},
		bytes.NewReader(content), blob.PutOptions{MaxSize: int64(len(content))})
	require.NoError(t, err)
	require.NoError(t, e.api.CompleteUpload(ctx, a, ref))
	a, err = e.api.ArtifactByToken(ctx, token)
	require.NoError(t, err)
	return a
}

// grant assigns the artifact to the device and mints its capability URL, the way the consumer does.
func (e *env) grant(t *testing.T, tenant, device, artifactToken string) (*model.UpdateAssignmentView, string) {
	t.Helper()
	ctx := ctxOf(tenant)
	v, err := e.api.AssignArtifact(ctx, device, artifactToken)
	require.NoError(t, err)
	g, err := e.api.GrantDownload(ctx, device, v.Assignment.Token)
	require.NoError(t, err)
	return v, g.URL
}

// rebase points a minted URL at the test server, keeping the path and query.
func (e *env) rebase(t *testing.T, minted string) string {
	t.Helper()
	u, err := url.Parse(minted)
	require.NoError(t, err)
	path := strings.TrimPrefix(u.Path, "/api/update-management")
	return e.srv.URL + path + "?" + u.RawQuery
}

func get(t *testing.T, target string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

func content(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func TestAGrantedDownloadServesTheWholeArtifact(t *testing.T) {
	e := newEnv(t, 4)
	data := content(100_000)
	a := e.artifact(t, "acme", "fw-1", data)
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")

	resp, body := get(t, e.rebase(t, minted))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, data, body)
	require.Equal(t, `"`+a.Digest+`"`, resp.Header.Get("ETag"))
	require.Equal(t, "bytes", resp.Header.Get("Accept-Ranges"))
	require.Equal(t, "100000", resp.Header.Get("Content-Length"))
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	// HEAD answers the same headers and no body.
	req, _ := http.NewRequest(http.MethodHead, e.rebase(t, minted), nil)
	hr, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer hr.Body.Close()
	hb, _ := io.ReadAll(hr.Body)
	require.Equal(t, http.StatusOK, hr.StatusCode)
	require.Empty(t, hb)
	require.Equal(t, "100000", hr.Header.Get("Content-Length"))
}

func TestRangeResumeReassemblesTheArtifactExactly(t *testing.T) {
	e := newEnv(t, 4)
	data := content(10_000)
	e.artifact(t, "acme", "fw-1", data)
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)

	cases := []struct {
		name, header string
		from, to     int // inclusive; the expected window
	}{
		{"open ended", "bytes=9000-", 9000, 9999},
		{"closed", "bytes=100-199", 100, 199},
		{"one byte", "bytes=0-0", 0, 0},
		{"last byte", "bytes=9999-9999", 9999, 9999},
		{"end clamped to the object", "bytes=9990-99999", 9990, 9999},
		{"suffix", "bytes=-250", 9750, 9999},
		{"suffix longer than the object", "bytes=-99999", 0, 9999},
	}
	for _, c := range cases {
		resp, body := get(t, target, "Range", c.header)
		require.Equal(t, http.StatusPartialContent, resp.StatusCode, c.name)
		require.Equal(t, data[c.from:c.to+1], body, c.name)
		require.Equal(t, fmt.Sprintf("bytes %d-%d/10000", c.from, c.to), resp.Header.Get("Content-Range"), c.name)
		require.Equal(t, fmt.Sprint(c.to-c.from+1), resp.Header.Get("Content-Length"), c.name)
	}

	// A device cut at an arbitrary point resumes at exactly the bytes it holds, repeatedly.
	var got []byte
	for _, step := range []int{1, 777, 4096, 1, 3000, 5000} {
		if len(got) >= len(data) {
			break
		}
		end := min(len(got)+step, len(data)) - 1
		resp, body := get(t, target, "Range", fmt.Sprintf("bytes=%d-%d", len(got), end))
		require.Equal(t, http.StatusPartialContent, resp.StatusCode)
		got = append(got, body...)
	}
	require.Equal(t, data, got)
}

func TestUnsatisfiableAndIgnoredRanges(t *testing.T) {
	e := newEnv(t, 4)
	data := content(1000)
	a := e.artifact(t, "acme", "fw-1", data)
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)

	for _, h := range []string{"bytes=1000-", "bytes=5000-6000", "bytes=-0", "bytes=10-5", "bytes=abc-", "bytes=0-1,5-6", "bytes=", "bytes=5"} {
		resp, _ := get(t, target, "Range", h)
		require.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.StatusCode, h)
		require.Equal(t, "bytes */1000", resp.Header.Get("Content-Range"), h)
	}
	// A unit this server does not know is ignored: the whole object.
	resp, body := get(t, target, "Range", "items=0-10")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, data, body)
	// If-Range that does not match the ETag falls back to the whole object, never a splice.
	resp, body = get(t, target, "Range", "bytes=10-19", "If-Range", `"sha256:`+strings.Repeat("0", 64)+`"`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, data, body)
	resp, body = get(t, target, "Range", "bytes=10-19", "If-Range", `"`+a.Digest+`"`)
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	require.Equal(t, data[10:20], body)
}

// forge rewrites one field of a capability's payload and keeps its signature.
func forge(t *testing.T, minted, field, value string) string {
	t.Helper()
	u, err := url.Parse(minted)
	require.NoError(t, err)
	capStr := u.Query().Get("cap")
	body, mac, _ := strings.Cut(capStr, ".")
	raw, err := base64.RawURLEncoding.DecodeString(body)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m[field] = value
	raw, err = json.Marshal(m)
	require.NoError(t, err)
	q := u.Query()
	q.Set("cap", base64.RawURLEncoding.EncodeToString(raw)+"."+mac)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestACapabilityIsRefusedBeforeAnyBytesAreOpened(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(5000))
	e.artifact(t, "acme", "fw-2", content(4000))
	e.artifact(t, "globex", "fw-1", content(3000))
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	good := e.rebase(t, minted)

	u, _ := url.Parse(minted)
	capStr := u.Query().Get("cap")
	flip := func(i int) string {
		b := []byte(capStr)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	withCap := func(c string) string {
		return e.srv.URL + "/updates/download/fw-1?cap=" + url.QueryEscape(c)
	}

	refusals := map[string]string{
		"no capability":                   e.srv.URL + "/updates/download/fw-1",
		"empty capability":                withCap(""),
		"garbage":                         withCap("not-a-capability"),
		"no signature":                    withCap(strings.Split(capStr, ".")[0]),
		"first byte of the payload":       withCap(flip(0)),
		"a byte of the payload":           withCap(flip(len(strings.Split(capStr, ".")[0]) / 2)),
		"first byte of the signature":     withCap(flip(len(strings.Split(capStr, ".")[0]) + 1)),
		"last byte of the signature":      withCap(flip(len(capStr) - 2)),
		"another device in the payload":   e.rebase(t, forge(t, minted, "d", "dev-2")),
		"another tenant in the payload":   e.rebase(t, forge(t, minted, "t", "globex")),
		"another assignment":              e.rebase(t, forge(t, minted, "a", "asg-0000")),
		"another digest":                  e.rebase(t, forge(t, minted, "g", "sha256:"+strings.Repeat("a", 64))),
		"another artifact in the payload": e.rebase(t, forge(t, minted, "x", "fw-2")),
		"another artifact's path":         e.srv.URL + "/updates/download/fw-2?cap=" + url.QueryEscape(capStr),
		"the capability twice":            good + "&cap=" + url.QueryEscape(capStr),
		"an oversized capability":         withCap(strings.Repeat("A", 5000)),
	}
	for name, target := range refusals {
		resp, body := get(t, target)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		require.NotContains(t, string(body), "firmware", name)
	}
	require.Zero(t, e.store.opens.Load(), "no refusal opened the artifact's bytes")

	// The genuine capability still works afterwards.
	resp, _ := get(t, good)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.EqualValues(t, 1, e.store.opens.Load())
}

func TestACapabilityExpires(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(1000))
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)

	e.clock = e.clock.Add(9*time.Minute + 59*time.Second)
	resp, _ := get(t, target)
	require.Equal(t, http.StatusOK, resp.StatusCode, "one second before expiry")

	e.clock = e.clock.Add(time.Second)
	resp, body := get(t, target)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "expired")

	// Asking again mints a fresh capability for the SAME assignment and artifact.
	v, err := e.api.AssignmentByToken(ctxOf("acme"), mustAssignmentToken(t, e, "dev-1"))
	require.NoError(t, err)
	g, err := e.api.GrantDownload(ctxOf("acme"), "dev-1", v.Assignment.Token)
	require.NoError(t, err)
	require.True(t, g.ExpiresAt.After(e.clock))
	resp, _ = get(t, e.rebase(t, g.URL))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, strings.Count(g.URL, "/fw-1?"), "the refreshed URL names the same artifact")

	// An expired capability whose signature is NOT genuine is not reported as expired: expiry is
	// only ever said of a capability that verified.
	forged := forge(t, minted, "e", "1")
	resp, body = get(t, e.rebase(t, forged))
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.NotContains(t, string(body), "expired")
}

func mustAssignmentToken(t *testing.T, e *env, device string) string {
	t.Helper()
	res, err := e.api.UpdateAssignments(ctxOf("acme"), model.UpdateAssignmentCriteria{
		DeviceToken: &device, CurrentOnly: true})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	return res.Results[0].Assignment.Token
}

func TestACapabilityDiesWithItsAssignment(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(1000))
	e.artifact(t, "acme", "fw-2", content(1100))
	v, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)

	// Cancel requested: the device is to stop, so it may not fetch more.
	_, err := e.api.ApplyReport(ctxOf("acme"), "dev-1", ota.Report{V: 1, Kind: ota.KindProgress,
		AttemptID: v.Attempt.Token, AssignmentID: v.Assignment.Token, Component: "firmware",
		ArtifactDigest: v.Attempt.TargetDigest, Seq: 1, Stage: ota.StageDownloading, BootID: "b"})
	require.NoError(t, err)
	_, err = e.api.CancelUpdate(ctxOf("acme"), v.Assignment.Token)
	require.NoError(t, err)
	resp, _ := get(t, target)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "cancel requested")
	_, err = e.api.ApplyReport(ctxOf("acme"), "dev-1", ota.Report{V: 1, Kind: ota.KindProgress,
		AttemptID: v.Attempt.Token, AssignmentID: v.Assignment.Token, Component: "firmware",
		ArtifactDigest: v.Attempt.TargetDigest, Seq: 2, Stage: ota.StageAbandoned, BootID: "b"})
	require.NoError(t, err)
	resp, _ = get(t, target)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "finished")

	// Superseded by a newer assignment: the old capability does not follow the device to it.
	_, minted2 := e.grant(t, "acme", "dev-1", "fw-2")
	resp, _ = get(t, target)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "superseded")
	resp, body := get(t, e.rebase(t, minted2))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, body, 1100)

	// A grant for another device's assignment token is refused: the device is part of the lookup.
	asg := mustAssignmentToken(t, e, "dev-1")
	_, err = e.api.GrantDownload(ctxOf("acme"), "dev-2", asg)
	var gr *model.GrantRefusal
	require.ErrorAs(t, err, &gr)
	require.Equal(t, model.GrantUnknownAssignment, gr.Reason)
	_, err = e.api.GrantDownload(ctxOf("globex"), "dev-1", asg)
	require.ErrorAs(t, err, &gr)
	require.Equal(t, model.GrantUnknownAssignment, gr.Reason)
}

func TestARetiredArtifactIsGone(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(1000))
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	_, err := e.api.RetireArtifact(ctxOf("acme"), "fw-1")
	require.NoError(t, err)
	resp, _ := get(t, e.rebase(t, minted))
	require.Equal(t, http.StatusGone, resp.StatusCode)
	require.Zero(t, e.store.opens.Load())
}

func TestADownloadStreamsInBoundedChunks(t *testing.T) {
	e := newEnv(t, 4)
	data := content(3 << 20)
	e.artifact(t, "acme", "fw-1", data)
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")

	resp, body := get(t, e.rebase(t, minted))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, bytes.Equal(data, body))
	require.LessOrEqual(t, e.store.maxRead.Load(), int64(chunkSize),
		"the artifact is read in chunks, never buffered whole")
	require.Positive(t, e.store.maxRead.Load())
}

func TestTooManyConcurrentDownloadsAreRefusedBeforeAnyWork(t *testing.T) {
	e := newEnv(t, 1)
	e.artifact(t, "acme", "fw-1", content(1000))
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)

	e.store.gate = make(chan struct{})
	e.store.started = make(chan struct{})
	// Released on any exit, so a failing assertion below fails the test at once instead of leaving
	// the first request parked on the gate (and the server waiting on it) until the suite times out.
	var once sync.Once
	release := func() { once.Do(func() { close(e.store.gate) }) }
	t.Cleanup(release)
	var wg sync.WaitGroup
	wg.Add(1)
	var first int
	go func() {
		defer wg.Done()
		resp, _ := get(t, target)
		first = resp.StatusCode
	}()
	select {
	case <-e.store.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first download never reached the store")
	}

	opensBefore := e.store.opens.Load()
	resp, _ := get(t, target)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "5", resp.Header.Get("Retry-After"))
	require.Equal(t, opensBefore, e.store.opens.Load(), "the refused request did no work")

	release()
	wg.Wait()
	require.Equal(t, http.StatusOK, first)
}

func TestResolveRangeTable(t *testing.T) {
	r := func(h string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		if h != "" {
			req.Header.Set("Range", h)
		}
		return req
	}
	s, n, partial, err := resolveRange(r(""), 10, `"x"`)
	require.NoError(t, err)
	require.Equal(t, [3]any{int64(0), int64(10), false}, [3]any{s, n, partial})
	s, n, partial, err = resolveRange(r("bytes=3-"), 10, `"x"`)
	require.NoError(t, err)
	require.Equal(t, [3]any{int64(3), int64(7), true}, [3]any{s, n, partial})
	_, _, _, err = resolveRange(r("bytes=10-"), 10, `"x"`)
	require.Error(t, err)
}

// A client that takes a byte now and then never trips the idle deadline, so the whole transfer has
// a ceiling of its own: past it the response is cut and the slot is given back.
func TestADownloadThatRunsPastItsWindowIsCut(t *testing.T) {
	e := newEnv(t, 1)
	data := content(1 << 20)
	e.artifact(t, "acme", "fw-1", data)
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	// Deterministic by construction: every read of the object takes longer than the whole window, so
	// by the time the first chunk is written the window has provably elapsed and the next loop
	// iteration must cut. Nothing depends on socket buffer sizes or on how fast the machine is.
	// The handler goroutine can outlive what the client sees, so the switch back to a normal window
	// is an atomic the already-installed hooks read, not a write to the handler's fields.
	var cutting atomic.Bool
	cutting.Store(true)
	e.h.window = func(length int64) time.Duration {
		if cutting.Load() {
			return 30 * time.Millisecond
		}
		return model.UploadWindow(length)
	}
	e.store.readDelay.Store(int64(60 * time.Millisecond))

	req, err := http.NewRequest(http.MethodGet, e.rebase(t, minted), nil)
	require.NoError(t, err)
	// The cut may land before the first byte (the write deadline is the window, and it has passed by
	// the time the slow first read returns) or after it; either way the client never gets the whole
	// artifact and the transfer reads as failed, never as a complete one.
	if resp, err := http.DefaultClient.Do(req); err == nil {
		defer resp.Body.Close()
		got, rerr := io.ReadAll(resp.Body)
		require.Error(t, rerr, "a cut response is a failed transfer, never a silently short artifact")
		require.Less(t, len(got), len(data), "the response was cut short of the artifact")
	}

	// The slot came back: with a normal window the next request is served in full.
	cutting.Store(false)
	e.store.readDelay.Store(0)
	require.Eventually(t, func() bool {
		r, body := get(t, e.rebase(t, minted))
		return r.StatusCode == http.StatusOK && len(body) == len(data)
	}, 5*time.Second, 50*time.Millisecond)
}

// 🔴 A CAPABILITY MINTED BEFORE A DEVICE WAS DISABLED DOES NOT OUTLIVE THE QUARANTINE. The
// device is mid-download on a connection nothing has closed: its next request is refused (403)
// before the first byte of the artifact is read, and enabling the device lets the same capability
// work again, because nothing was recorded against it.
func TestACapabilityIsRefusedWhileItsDeviceIsDisabled(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(1000))
	_, minted := e.grant(t, "acme", "dev-1", "fw-1")
	target := e.rebase(t, minted)
	resp, _ := get(t, target)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	e.devices.disabled["acme/dev-1"] = true
	e.clock = e.clock.Add(model.DisabledCacheTTL) // the disabled answer is remembered for at most this long
	opens := e.store.opens.Load()
	resp, _ = get(t, target)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "a disabled device was served")
	require.Equal(t, opens, e.store.opens.Load(), "the artifact was opened for a disabled device")

	delete(e.devices.disabled, "acme/dev-1")
	e.clock = e.clock.Add(model.DisabledCacheTTL)
	resp, body := get(t, target)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, body, 1000)
}
