// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package upload

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type profiles struct{}

func (profiles) Missing(context.Context, string, []string) ([]string, error) { return nil, nil }

type env struct {
	t      *testing.T
	api    *model.Api
	store  blob.Store
	dir    string
	issuer *auth.Issuer
	val    *auth.Validator
	srv    *httptest.Server
	h      *Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, schema.NewBaselineSchema().Migrate(db))
	require.NoError(t, schema.NewArtifactsSchema().Migrate(db))

	dir := t.TempDir()
	store, err := blob.New(context.Background(), blob.Config{Backend: blob.BackendFilesystem, Directory: dir}, "inst")
	require.NoError(t, err)
	api := model.NewApi(&rdb.RdbManager{Database: db}, store, model.Limits{
		MaxArtifactBytes: 64 << 20, ConfiguredMaxArtifactBytes: 64 << 20, MaxTenantArtifactBytes: 1 << 30,
	})
	api.Profiles = profiles{}

	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	e := &env{t: t, api: api, store: store, dir: dir,
		issuer: auth.NewIssuer(key, "https://as.example.com", time.Hour, time.Hour),
		val:    auth.NewValidator(&key.PublicKey)}
	e.h = New(api, func() *auth.Validator { return e.val })
	mux := http.NewServeMux()
	mux.Handle(Path, e.h)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) token(tenant string, authorities ...auth.Authority) string {
	as := make([]string, len(authorities))
	for i, a := range authorities {
		as[i] = string(a)
	}
	tok, err := e.issuer.IssueTenantAccess(tenant, "a@b.c", nil, as, false, "j-"+tenant)
	require.NoError(e.t, err)
	return tok.Token
}

func (e *env) writer(tenant string) string { return e.token(tenant, auth.ArtifactWrite) }

func ctxFor(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return model.DigestPrefix + hex.EncodeToString(sum[:])
}

// declare creates an UPLOADING artifact for content in tenant.
func (e *env) declare(tenant, token string, content []byte) *model.Artifact {
	e.t.Helper()
	a, err := e.api.CreateArtifact(ctxFor(tenant), &model.ArtifactCreateRequest{
		Token: token, Kind: model.KindFirmware, Name: "fw-" + token, Version: "1", Component: "firmware",
		SizeBytes: int64(len(content)), Digest: digestOf(content), CompatibleProfiles: []string{"gateway"},
	})
	require.NoError(e.t, err)
	return a
}

func (e *env) state(tenant, token string) *model.Artifact {
	e.t.Helper()
	a, err := e.api.ArtifactByToken(ctxFor(tenant), token)
	require.NoError(e.t, err)
	require.NotNil(e.t, a)
	return a
}

// objects counts the committed objects in the store (temp files of an in-flight Put
// are ignored; a failed Put removes its own).
func (e *env) objects() []string {
	e.t.Helper()
	var found []string
	require.NoError(e.t, filepath.WalkDir(e.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, path)
		}
		return nil
	}))
	return found
}

func (e *env) url(token string) string { return e.srv.URL + "/updates/artifacts/" + token + "/content" }

func (e *env) put(bearer, token string, body io.Reader) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.url(token), body)
	require.NoError(e.t, err)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	t := e.t
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func status(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	require.Equal(t, want, resp.StatusCode, "body: %s", strings.TrimSpace(string(b)))
}

func TestUploadHappyPathStoresVerifiedBytesAndMarksAvailable(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("firmware-image-"), 40_000) // ~600 KB, several read chunks
	e.declare("acme", "fw-1", content)

	resp := e.put(e.writer("acme"), "fw-1", bytes.NewReader(content))
	status(t, resp, http.StatusOK)

	a := e.state("acme", "fw-1")
	require.Equal(t, model.StateAvailable, a.State)
	require.True(t, a.BlobRef.Valid)
	ref, err := blob.ParseRef(a.BlobRef.String)
	require.NoError(t, err)
	rc, info, err := e.store.Open(ctxFor("acme"), ref)
	require.NoError(t, err)
	defer rc.Close()
	stored, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, content, stored)
	require.Equal(t, int64(len(content)), info.Size)
	require.Equal(t, "application/octet-stream", info.ContentType)
	require.Contains(t, ref.Key, "/acme/"+model.BlobPurpose+"/", "stored under the tenant's own prefix")
	require.Len(t, e.objects(), 1)
}

// A chunked body (no Content-Length) of exactly the declared size is accepted: the length
// is enforced while streaming, not demanded up front.
func TestUploadAcceptsAChunkedBodyOfTheDeclaredSize(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("x"), 300_000)
	e.declare("acme", "fw-1", content)
	// io.MultiReader hides the length, so the client sends Transfer-Encoding: chunked.
	resp := e.put(e.writer("acme"), "fw-1", io.MultiReader(bytes.NewReader(content)))
	status(t, resp, http.StatusOK)
	require.Equal(t, model.StateAvailable, e.state("acme", "fw-1").State)
}

// ---- size enforcement: before the stream, and during it ----

// A Content-Length over the declared size is refused before one byte of the body is read.
func TestOversizedContentLengthIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", bytes.Repeat([]byte("a"), 1000))

	got := rawPut(t, e.srv, "fw-1", e.writer("acme"), "Content-Length: 5000000", []byte("only a few bytes"))
	require.Equal(t, http.StatusRequestEntityTooLarge, got.status)
	require.Empty(t, e.objects())
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
}

// A body that lies about its length is cut at the declared size: with no Content-Length
// (chunked) there is nothing to refuse up front, so the stream itself must stop.
func TestAChunkedBodyPastTheDeclaredSizeIsCutAndNothingIsStored(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", bytes.Repeat([]byte("a"), 1000))

	req, err := http.NewRequest(http.MethodPut, e.url("fw-1"), io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("a"), 8<<20))))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	} else {
		// The server may close the connection before the client finishes sending.
		require.True(t, errors.Is(err, io.EOF) || strings.Contains(err.Error(), "reset") ||
			strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "aborted"), "%v", err)
	}
	require.Empty(t, e.objects(), "an over-long body must leave no object behind")
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
}

// The stream reader never pulls more than one byte past the limit from its source, however
// much the source could supply.
func TestStreamReaderConsumesAtMostLimitPlusOne(t *testing.T) {
	src := &endless{}
	sr := &streamReader{src: src, hash: sha256.New(), max: 1000, ctx: context.Background(),
		rc: http.NewResponseController(httptest.NewRecorder()), idle: time.Second, deadline: time.Now().Add(time.Minute)}
	_, err := io.Copy(io.Discard, sr)
	require.ErrorIs(t, err, errBodyTooLong)
	require.EqualValues(t, 1001, src.served, "exactly max+1 bytes are read before the stream is cut")
}

type endless struct{ served int64 }

func (e *endless) Read(p []byte) (int, error) {
	e.served += int64(len(p))
	return len(p), nil
}

// ---- authentication and authorization happen before the body is touched ----

// panicBody fails the test the moment anything reads it.
type panicBody struct{ t *testing.T }

func (b panicBody) Read([]byte) (int, error) {
	b.t.Helper()
	b.t.Error("the request body was read before the request was authorized")
	return 0, io.EOF
}
func (panicBody) Close() error { return nil }

func TestRefusalsHappenWithoutReadingTheBody(t *testing.T) {
	e := newEnv(t)
	content := []byte("0123456789")
	e.declare("acme", "fw-1", content)
	e.declare("acme", "fw-done", []byte("done"))
	_, err := e.api.RetireArtifact(ctxFor("acme"), "fw-done")
	require.NoError(t, err)

	serviceTok, err := e.issuer.IssueService("some-service", []string{string(auth.ArtifactWrite)}, "j-svc")
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		bearer string
		token  string
		cl     int64
		want   int
	}{
		{"no token", "", "fw-1", int64(len(content)), http.StatusUnauthorized},
		{"garbage token", "not.a.jwt", "fw-1", int64(len(content)), http.StatusUnauthorized},
		{"service token", serviceTok.Token, "fw-1", int64(len(content)), http.StatusUnauthorized},
		{"no authority", e.token("acme"), "fw-1", int64(len(content)), http.StatusForbidden},
		{"read-only authority", e.token("acme", auth.ArtifactRead), "fw-1", int64(len(content)), http.StatusForbidden},
		{"another tenant", e.writer("globex"), "fw-1", int64(len(content)), http.StatusNotFound},
		{"unknown artifact", e.writer("acme"), "ghost", int64(len(content)), http.StatusNotFound},
		{"retired artifact", e.writer("acme"), "fw-done", 4, http.StatusConflict},
		{"content-length too large", e.writer("acme"), "fw-1", 1 << 30, http.StatusRequestEntityTooLarge},
		{"content-length too small", e.writer("acme"), "fw-1", 3, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/updates/artifacts/"+tc.token+"/content", panicBody{t})
			req.SetPathValue("token", tc.token)
			req.ContentLength = tc.cl
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			require.Equal(t, "close", rec.Header().Get("Connection"), "an unread body must not be drained")
			require.Empty(t, e.objects())
		})
	}
}

// End to end over a real connection: an unauthenticated caller that declares a large body
// and then goes quiet gets its 401 at once, instead of being waited on.
func TestUnauthenticatedAndUnauthorizedStalledUploadsAreAnsweredAtOnce(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", []byte("0123456789"))

	for name, bearer := range map[string]string{
		"unauthenticated": "",
		"unauthorized":    e.token("acme", auth.ArtifactRead),
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			got := rawPut(t, e.srv, "fw-1", bearer, "Content-Length: 10", nil)
			require.Less(t, time.Since(started), 2*time.Second, "the server waited on a body it should never read")
			if bearer == "" {
				require.Equal(t, http.StatusUnauthorized, got.status)
			} else {
				require.Equal(t, http.StatusForbidden, got.status)
			}
		})
	}
}

func TestAnAuthorizedStalledUploadIsAbandonedAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("a"), 100_000)
	e.declare("acme", "fw-1", content)
	e.h.idle = 150 * time.Millisecond

	started := time.Now()
	// Declares the real length, sends 10 bytes, then stalls.
	got := rawPut(t, e.srv, "fw-1", e.writer("acme"), fmt.Sprintf("Content-Length: %d", len(content)), []byte("0123456789"))
	require.Less(t, time.Since(started), 3*time.Second)
	require.Equal(t, http.StatusRequestTimeout, got.status)
	require.Empty(t, e.objects())
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
}

func TestWrongMethodAndUnavailableAuthAreRefused(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", []byte("data"))

	req, _ := http.NewRequest(http.MethodPost, e.url("fw-1"), strings.NewReader("data"))
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	// The validator is bound late: until it is, a presented token cannot be trusted.
	tok := e.writer("acme")
	e.val = nil
	e.h.validator = func() *auth.Validator { return nil }
	resp = e.put(tok, "fw-1", strings.NewReader("data"))
	status(t, resp, http.StatusUnauthorized)
}

func TestADeletedTenantCannotUpload(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", []byte("data"))
	e.api.TenantDeleted = func(tenant string) bool { return tenant == "acme" }
	status(t, e.put(e.writer("acme"), "fw-1", strings.NewReader("data")), http.StatusForbidden)
	require.Empty(t, e.objects())
}

func TestNoObjectStoreIsServiceUnavailable(t *testing.T) {
	e := newEnv(t)
	e.declare("acme", "fw-1", []byte("data"))
	e.api.Blobs = nil
	status(t, e.put(e.writer("acme"), "fw-1", strings.NewReader("data")), http.StatusServiceUnavailable)
}

// ---- verification: size and digest, with cleanup ----

func TestDigestMismatchDeletesTheObjectAndLeavesTheArtifactUploading(t *testing.T) {
	e := newEnv(t)
	declared := bytes.Repeat([]byte("good"), 1000)
	e.declare("acme", "fw-1", declared)

	tampered := bytes.Repeat([]byte("evil"), 1000) // same size, different bytes
	require.Len(t, tampered, len(declared))
	resp := e.put(e.writer("acme"), "fw-1", bytes.NewReader(tampered))
	status(t, resp, http.StatusUnprocessableEntity)

	require.Empty(t, e.objects(), "a rejected upload must leave nothing in the object store")
	a := e.state("acme", "fw-1")
	require.Equal(t, model.StateUploading, a.State)
	require.False(t, a.BlobRef.Valid)

	// The artifact is still uploadable: the correct bytes now succeed.
	status(t, e.put(e.writer("acme"), "fw-1", bytes.NewReader(declared)), http.StatusOK)
	require.Equal(t, model.StateAvailable, e.state("acme", "fw-1").State)
	require.Len(t, e.objects(), 1)
}

func TestAShortBodyIsRefusedAndCleanedUp(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("a"), 5000)
	e.declare("acme", "fw-1", content)

	// Chunked, so no Content-Length can refuse it up front: the body simply ends early.
	resp := e.put(e.writer("acme"), "fw-1", io.MultiReader(bytes.NewReader(content[:4000])))
	status(t, resp, http.StatusBadRequest)
	require.Empty(t, e.objects(), "a short body must not leave a stored object")
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
}

func TestAConnectionThatDropsMidUploadStoresNothing(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("a"), 100_000)
	e.declare("acme", "fw-1", content)

	conn, err := net.Dial("tcp", e.srv.Listener.Addr().String())
	require.NoError(t, err)
	fmt.Fprintf(conn, "PUT /updates/artifacts/fw-1/content HTTP/1.1\r\nHost: t\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n",
		e.writer("acme"), len(content))
	_, _ = conn.Write(content[:30_000])
	_ = conn.Close()

	require.Eventually(t, func() bool { return len(e.objects()) == 0 }, 2*time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	require.Empty(t, e.objects())
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)
}

// ---- immutability ----

func TestAnAvailableArtifactCannotBeUploadedOver(t *testing.T) {
	e := newEnv(t)
	content := []byte("the one true image")
	e.declare("acme", "fw-1", content)
	status(t, e.put(e.writer("acme"), "fw-1", bytes.NewReader(content)), http.StatusOK)
	before := e.state("acme", "fw-1")

	status(t, e.put(e.writer("acme"), "fw-1", bytes.NewReader(content)), http.StatusConflict)
	after := e.state("acme", "fw-1")
	require.Equal(t, before.BlobRef, after.BlobRef, "the stored object is untouched")
	require.Len(t, e.objects(), 1)
}

// Many simultaneous uploads of the same artifact: exactly one wins, the losers' objects
// are removed, and what remains is a single object.
func TestConcurrentUploadsOfOneArtifactLeaveExactlyOneObject(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("c"), 2<<20)
	e.declare("acme", "fw-1", content)
	tok := e.writer("acme")

	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodPut, e.url("fw-1"), bytes.NewReader(content))
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes <- -1
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	ok := 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	require.Equal(t, 1, ok, "exactly one concurrent upload may win")
	require.Len(t, e.objects(), 1, "the losers' objects must be deleted")
	require.Equal(t, model.StateAvailable, e.state("acme", "fw-1").State)
}

// ---- tenant isolation ----

func TestATenantCannotUploadToAnotherTenantsArtifact(t *testing.T) {
	e := newEnv(t)
	content := []byte("acme private firmware")
	e.declare("acme", "fw-1", content)

	status(t, e.put(e.writer("globex"), "fw-1", bytes.NewReader(content)), http.StatusNotFound)
	require.Empty(t, e.objects())
	require.Equal(t, model.StateUploading, e.state("acme", "fw-1").State)

	// And the object a legitimate upload makes is under the owner's prefix only.
	status(t, e.put(e.writer("acme"), "fw-1", bytes.NewReader(content)), http.StatusOK)
	for _, o := range e.objects() {
		require.Contains(t, filepath.ToSlash(o), "/acme/")
		require.NotContains(t, filepath.ToSlash(o), "/globex/")
	}
}

// ---- streaming ----

// A large upload does not buffer the body: the bytes allocated across the whole round trip
// (client and server) stay a small fraction of the artifact's size. A handler that read
// the body into memory first would allocate at least its size.
func TestALargeUploadIsStreamedNotBuffered(t *testing.T) {
	e := newEnv(t)
	const size = 48 << 20
	// Built from one block so the test itself holds a 1 MiB block, not the artifact.
	block := bytes.Repeat([]byte("0123456789abcdef"), 64<<10)
	h := sha256.New()
	for i := 0; i < size/len(block); i++ {
		h.Write(block)
	}
	digest := model.DigestPrefix + hex.EncodeToString(h.Sum(nil))
	_, err := e.api.CreateArtifact(ctxFor("acme"), &model.ArtifactCreateRequest{
		Token: "big", Kind: model.KindFirmware, Name: "big", Version: "1", Component: "firmware",
		SizeBytes: size, Digest: digest, CompatibleProfiles: []string{"gateway"},
	})
	require.NoError(t, err)

	body := &repeatReader{block: block, total: size}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	req, err := http.NewRequest(http.MethodPut, e.url("big"), body)
	require.NoError(t, err)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+e.writer("acme"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	status(t, resp, http.StatusOK)
	runtime.ReadMemStats(&after)

	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("uploaded %d MiB, allocated %d KiB", size>>20, allocated>>10)
	require.Less(t, allocated, uint64(size/32),
		"uploading %d MiB allocated %d MiB: the body is being buffered", size>>20, allocated>>20)
	require.Equal(t, model.StateAvailable, e.state("acme", "big").State)
}

type repeatReader struct {
	block []byte
	total int64
	sent  int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.sent >= r.total {
		return 0, io.EOF
	}
	n := copy(p, r.block)
	if rem := r.total - r.sent; int64(n) > rem {
		n = int(rem)
	}
	r.sent += int64(n)
	return n, nil
}

// ---- raw-connection helper ----

type rawResult struct{ status int }

// rawPut sends a PUT with exactly the given extra header line and body bytes and then
// goes quiet, returning the response the server gives without being sent the rest. If the
// server waits for a body it was never going to read, the read deadline fails the test.
func rawPut(t *testing.T, srv *httptest.Server, token, bearer, lengthHeader string, body []byte) rawResult {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	var sb strings.Builder
	fmt.Fprintf(&sb, "PUT /updates/artifacts/%s/content HTTP/1.1\r\nHost: t\r\n", token)
	if bearer != "" {
		fmt.Fprintf(&sb, "Authorization: Bearer %s\r\n", bearer)
	}
	fmt.Fprintf(&sb, "%s\r\n\r\n", lengthHeader)
	_, err = conn.Write(append([]byte(sb.String()), body...))
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "no response: the server is waiting on the body")
	_ = resp.Body.Close()
	return rawResult{status: resp.StatusCode}
}
