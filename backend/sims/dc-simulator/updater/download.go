// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors the HTTP source returns. The client reads them to decide whether to ask the platform for a
// new download grant (the first two) or to retry the same one (a transport error).
var (
	// ErrNoGrant: the device holds no usable download grant, or the one it holds has expired by the
	// device's own clock.
	ErrNoGrant = errors.New("updater: no usable download grant")
	// ErrGrantRefused: the platform refused the grant (403/410): it expired by the platform's
	// clock, or the assignment is no longer current. The device asks again; if the assignment is
	// gone the platform answers with a cancel instead of a new grant.
	ErrGrantRefused = errors.New("updater: the download grant was refused")
	// ErrRangeIgnored: the platform answered a ranged request with the whole object, so a resume
	// cannot be trusted. It is an error rather than a quiet fallback because splicing a whole
	// object onto a partial one would produce a corrupt image that only the final hash would catch.
	ErrRangeIgnored = errors.New("updater: the platform ignored the Range header")
)

// grant is a capability URL and when the device stops trusting it.
type grant struct {
	url     string
	expires time.Time
}

// grantBox holds the device current grant. The source reads it, the client writes it.
type grantBox struct {
	mu  sync.Mutex
	g   *grant
	now func() time.Time
}

func (b *grantBox) set(g grant) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.g = &g
}

func (b *grantBox) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.g = nil
}

func (b *grantBox) current() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.g == nil || !b.now().Before(b.g.expires) {
		return "", ErrNoGrant
	}
	return b.g.url, nil
}

// httpSource is an io.ReaderAt over the platform download endpoint. Every ReadAt is one ranged GET
// at the offset the updater persisted, which is what makes a download resumable: there is no
// connection to resume, only the next window to ask for.
type httpSource struct {
	client *http.Client
	grants *grantBox
}

// ReadAt reads len(p) bytes at off with `Range: bytes=off-(off+len(p)-1)`. It returns
// io.ErrUnexpectedEOF when the body ends before the window does, as a connection cut mid-body does,
// and the bytes read so far are NOT committed by the updater, so the next call re-reads the window.
func (s *httpSource) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	target, err := s.grants.current()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("updater: download at offset %d: %w", off, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusForbidden, http.StatusGone:
		s.grants.clear()
		return 0, fmt.Errorf("%w (HTTP %d)", ErrGrantRefused, resp.StatusCode)
	case http.StatusOK:
		if off != 0 {
			return 0, ErrRangeIgnored
		}
	default:
		return 0, fmt.Errorf("updater: download at offset %d: HTTP %d", off, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusPartialContent {
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != off {
			return 0, fmt.Errorf("updater: download at offset %d: the platform answered for another window (%q)",
				off, resp.Header.Get("Content-Range"))
		}
	}
	n, err := io.ReadFull(resp.Body, p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return n, fmt.Errorf("updater: download at offset %d: %w", off, err)
	}
	return n, nil
}

// contentRangeStart reads the first byte of a `bytes a-b/total` Content-Range.
func contentRangeStart(h string) (int64, bool) {
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	return v, err == nil
}
