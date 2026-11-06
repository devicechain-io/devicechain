// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package otafleet

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rangeServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x", time.Time{}, strings.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A bad-hash device is served bad bytes in the first window it fetches, wherever that starts: a
// device resumed from a persisted state directory begins at a nonzero offset.
func TestBadHashCorruptsTheFirstWindowWhateverItsOffset(t *testing.T) {
	body := strings.Repeat("abcdefgh", 1000)
	srv := rangeServer(t, body)
	ft := &faultTransport{next: http.DefaultTransport, mode: BadHash}
	c := &http.Client{Transport: ft}

	get := func(from, to int) string {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	first := get(4096, 4103)
	assert.NotEqual(t, body[4096:4104], first, "the first window, at a nonzero offset, must be corrupted")
	assert.Equal(t, body[4104:4112], get(4104, 4111), "later windows are untouched")
	assert.Equal(t, 1, int(ft.corrupted.Load()))
}

// A capability URL naming the instance's host on the wrong port is dialled at the endpoint's own
// address; a different host is left alone.
func TestDownloadTransportRedirectsTheSameHostToTheEndpointPort(t *testing.T) {
	srv := rangeServer(t, "payload")
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tr := DownloadTransport("", "127.0.0.1:"+port, false)
	c := &http.Client{Transport: tr}

	resp, err := c.Get("http://127.0.0.1:1/x") // nothing listens on port 1
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "payload", string(b))

	_, err = c.Get("http://127.0.0.2:1/x")
	assert.Error(t, err, "another host must not be redirected")
}
