// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package download_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/download"
)

var testModTime = time.Unix(1_700_000_000, 0)

var payload = bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB

type rangeLog struct {
	mu     sync.Mutex
	ranges []string
}

func (l *rangeLog) add(r string) { l.mu.Lock(); l.ranges = append(l.ranges, r); l.mu.Unlock() }
func (l *rangeLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ranges...)
}

func fetch(t *testing.T, url string, size int64, part string) error {
	t.Helper()
	return download.Fetch(context.Background(), url, download.Options{Size: size, Part: part})
}

func TestFetchesWholeFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "img", testModTime, bytes.NewReader(payload))
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")
	var last int64
	require.NoError(t, download.Fetch(context.Background(), srv.URL, download.Options{Size: int64(len(payload)), Part: part, OnBytes: func(n int64) { last = n }}))
	got, _ := os.ReadFile(part)
	assert.Equal(t, payload, got)
	assert.Equal(t, int64(len(payload)), last)
}

// A connection cut at 40% leaves a partial file; the next call asks for exactly the bytes after it
// and only appends those.
func TestResumesFromTheBytesHeld(t *testing.T) {
	var log rangeLog
	var cut atomic.Bool
	cut.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Header.Get("Range"))
		if cut.Swap(false) {
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:len(payload)*4/10])
			w.(http.Flusher).Flush()
			hj, _, _ := w.(http.Hijacker).Hijack()
			_ = hj.Close() // the body ends early
			return
		}
		http.ServeContent(w, r, "img", testModTime, bytes.NewReader(payload))
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")

	err := fetch(t, srv.URL, int64(len(payload)), part)
	require.Error(t, err)
	assert.True(t, errors.Is(err, download.ErrInterrupted), "%v", err)
	fi, _ := os.Stat(part)
	held := fi.Size()
	assert.Equal(t, int64(len(payload)*4/10), held, "the partial bytes are kept")

	require.NoError(t, fetch(t, srv.URL, int64(len(payload)), part))
	assert.Equal(t, []string{"", fmt.Sprintf("bytes=%d-", held)}, log.get(), "the second request resumes at the offset held")
	got, _ := os.ReadFile(part)
	assert.Equal(t, payload, got)
}

func TestRefusesAWrongContentRangeStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.WriteFile(part, payload[:100], 0o600))
	err := fetch(t, srv.URL, int64(len(payload)), part)
	assert.True(t, errors.Is(err, download.ErrBadRange), "%v", err)
	fi, _ := os.Stat(part)
	assert.Equal(t, int64(100), fi.Size(), "nothing was appended from the wrong range")
}

func TestOneByteTooManyIsASizeMismatchAndTheFileIsDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no Content-Length: chunked, so only the reader can notice the extra byte
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")
	err := fetch(t, srv.URL, int64(len(payload)), part)
	assert.True(t, errors.Is(err, download.ErrSizeMismatch), "%v", err)
	_, statErr := os.Stat(part)
	assert.True(t, os.IsNotExist(statErr), "an over-long download leaves nothing behind")
}

func TestServerTotalThatDisagreesWithTheAssignment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 100-%d/%d", len(payload)-1, len(payload)+7))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[100:])
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.WriteFile(part, payload[:100], 0o600))
	err := fetch(t, srv.URL, int64(len(payload)), part)
	assert.True(t, errors.Is(err, download.ErrSizeMismatch), "%v", err)
}

func TestRejectedCapability(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		err := fetch(t, srv.URL, 10, filepath.Join(t.TempDir(), "p"))
		srv.Close()
		assert.True(t, errors.Is(err, download.ErrGrantRejected), "%d: %v", code, err)
	}
}

func TestAServerThatIgnoresRangeRestartsFromZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload) // always a plain 200 from byte 0
	}))
	defer srv.Close()
	part := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.WriteFile(part, []byte("stale-partial"), 0o600))
	require.NoError(t, fetch(t, srv.URL, int64(len(payload)), part))
	got, _ := os.ReadFile(part)
	assert.Equal(t, payload, got, "the stale prefix is not glued onto a full body")
}

func TestAlreadyCompleteAndOversizedPartials(t *testing.T) {
	part := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.WriteFile(part, payload, 0o600))
	// complete: no request is made at all (the URL is unusable on purpose)
	require.NoError(t, fetch(t, "http://127.0.0.1:1/never", int64(len(payload)), part))
	// larger than declared: it belongs to some other image and is discarded
	err := fetch(t, "http://127.0.0.1:1/never", int64(len(payload)-1), part)
	assert.Error(t, err)
	_, statErr := os.Stat(part)
	assert.True(t, os.IsNotExist(statErr))
}

// The capability is a bearer secret in the URL. No error this package returns may carry it.
func TestErrorsNeverCarryTheCapability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := srv.URL + "/updates/download/art?cap=TOPSECRETCAP"
	srv.Close() // nothing is listening: a transport error that would normally quote the URL
	err := fetch(t, u, 10, filepath.Join(t.TempDir(), "p"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "TOPSECRETCAP")
	assert.False(t, strings.Contains(err.Error(), "cap="), err.Error())
}
