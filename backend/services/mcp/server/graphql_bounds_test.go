// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type memoryRoundTripper func(*http.Request) (*http.Response, error)

func (f memoryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedResponseReader struct {
	io.Reader
	read int
}

func (r *countedResponseReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

// Refusing after an unbounded ReadAll would still OOM. Observe bytes consumed,
// using a finite valid response so a broken bound fails without exhausting RAM.
func TestGraphQLResponseReadStopsAtMemoryCeiling(t *testing.T) {
	body := &countedResponseReader{Reader: strings.NewReader(`{"data":{}}` + strings.Repeat(" ", 1<<20))}
	c := NewGraphQLClient()
	c.http = &http.Client{Transport: memoryRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
	})}
	err := c.Query(context.Background(), listDevicesQuery, "token", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "narrow the request") {
		t.Fatalf("expected memory-limit refusal, got %v", err)
	}
	if body.read != (256<<10)+1 {
		t.Fatalf("read %d bytes; want exactly 256 KiB plus the overflow byte", body.read)
	}
}
