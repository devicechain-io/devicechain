// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// source is one pinned upstream input.
type source struct {
	Name   string
	SHA256 string
	URL    string
	Data   []byte
}

// lockEntry is one line of hack/vocab/sources.lock: `name sha256 url`.
type lockEntry struct {
	Name, SHA256, URL string
}

// parseLock reads the lock. A malformed line, a duplicate name or a digest that
// is not 64 hex characters is an error: a lock that is silently partial pins
// less than it appears to.
func parseLock(r io.Reader) (map[string]lockEntry, error) {
	out := map[string]lockEntry{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("lock line %d: want `name sha256 url`, got %d fields", n, len(f))
		}
		e := lockEntry{Name: f[0], SHA256: strings.ToLower(f[1]), URL: f[2]}
		if len(e.SHA256) != 64 {
			return nil, fmt.Errorf("lock line %d: %s: sha256 must be 64 hex characters", n, e.Name)
		}
		if _, err := hex.DecodeString(e.SHA256); err != nil {
			return nil, fmt.Errorf("lock line %d: %s: sha256 is not hex", n, e.Name)
		}
		if !strings.HasPrefix(e.URL, "https://") {
			return nil, fmt.Errorf("lock line %d: %s: url must be https", n, e.Name)
		}
		if _, dup := out[e.Name]; dup {
			return nil, fmt.Errorf("lock line %d: %s is listed twice", n, e.Name)
		}
		out[e.Name] = e
	}
	return out, sc.Err()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// loader hands out hash-verified inputs. A hash mismatch is an error that names
// both digests; it never falls through to using the bytes anyway.
type loader struct {
	lock  map[string]lockEntry
	cache string
	fetch bool
}

func (l *loader) get(name string) (source, error) {
	e, ok := l.lock[name]
	if !ok {
		return source{}, fmt.Errorf("input %q is not in the lock", name)
	}
	path := filepath.Join(l.cache, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return source{}, err
		}
		if !l.fetch {
			return source{}, fmt.Errorf("input %q is not in the cache %s (re-run with -fetch to download it from %s)", name, l.cache, e.URL)
		}
		data, err = download(e.URL)
		if err != nil {
			return source{}, fmt.Errorf("fetch %s: %w", name, err)
		}
		// Verify BEFORE caching, so a bad download cannot poison later runs.
		if got := sha256Hex(data); got != e.SHA256 {
			return source{}, hashMismatch(name, e, got)
		}
		if err := os.MkdirAll(l.cache, 0o755); err != nil {
			return source{}, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return source{}, err
		}
	}
	if got := sha256Hex(data); got != e.SHA256 {
		return source{}, hashMismatch(name, e, got)
	}
	return source{Name: name, SHA256: e.SHA256, URL: e.URL, Data: data}, nil
}

func hashMismatch(name string, e lockEntry, got string) error {
	return fmt.Errorf("input %q does not match the lock\n  url:      %s\n  expected: %s\n  got:      %s\n"+
		"an upstream file changed under a pinned URL, or the cache is stale; do not update the lock until you have read the diff",
		name, e.URL, e.SHA256, got)
}

func download(url string) ([]byte, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
