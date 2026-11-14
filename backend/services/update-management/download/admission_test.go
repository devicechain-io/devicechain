// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package download

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type parkedWriter struct {
	*httptest.ResponseRecorder
	started, resume chan struct{}
}

func (w *parkedWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.resume
	return w.ResponseRecorder.Write(p)
}

func TestVerifiedDownloadShares(t *testing.T) {
	e := newEnv(t, 4)
	e.devices.profiles["acme/dev-3"] = "gateway"
	e.artifact(t, "acme", "fw-1", content(1000))
	e.artifact(t, "globex", "fw-1", content(1000))
	var urls []string
	var first *model.UpdateAssignmentView
	for _, device := range []string{"dev-1", "dev-2", "dev-3"} {
		v, u := e.grant(t, "acme", device, "fw-1")
		if first == nil {
			first = v
		}
		urls = append(urls, e.rebase(t, u))
	}
	_, other := e.grant(t, "globex", "dev-1", "fw-1")
	e.h.metrics = &Metrics{Requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_download_requests"}, []string{"result"})}
	park := func(target string) {
		w := &parkedWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.SetPathValue("artifact", "fw-1")
		done := make(chan struct{})
		go func() { defer close(done); e.h.ServeHTTP(w, r) }()
		t.Cleanup(func() { close(w.resume); <-done })
		select {
		case <-w.started:
		case <-done:
			t.Fatal("download refused before streaming")
		case <-time.After(5 * time.Second):
			t.Fatal("download never started")
		}
	}
	busy := func(target string) {
		before := e.store.opens.Load()
		resp, _ := get(t, target)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		require.Equal(t, "5", resp.Header.Get("Retry-After"))
		require.Equal(t, before, e.store.opens.Load())
	}
	park(urls[0])
	busy(urls[0]) // the same capability cannot occupy a second slot
	// A newly signed capability still names the same admission key.
	e.clock = e.clock.Add(time.Second)
	g, err := e.api.GrantDownload(ctxOf("acme"), "dev-1", first.Assignment.Token)
	require.NoError(t, err)
	busy(e.rebase(t, g.URL))
	park(urls[1])
	busy(urls[2]) // two different devices already hold this tenant's half
	resp, _ := get(t, e.rebase(t, other))
	require.Equal(t, http.StatusOK, resp.StatusCode, "another tenant retains capacity")
	require.Equal(t, float64(3), testutil.ToFloat64(e.h.metrics.Requests.WithLabelValues(ResultBusy)))
}

func TestDownloadAdmissionKeysAndRelease(t *testing.T) {
	h := New(nil, 16, nil)
	a := model.Capability{Tenant: "a", DeviceToken: "d", AssignmentID: "x"}
	require.True(t, h.admit(a))
	require.False(t, h.admit(a))
	for _, c := range []model.Capability{
		{Tenant: "b", DeviceToken: "d", AssignmentID: "x"},
		{Tenant: "a", DeviceToken: "e", AssignmentID: "x"},
		{Tenant: "a", DeviceToken: "d", AssignmentID: "y"},
	} {
		require.True(t, h.admit(c), "all three identity fields scope the key")
		h.release(c)
	}
	h.release(a)
	require.Empty(t, h.tenants)
	require.Empty(t, h.streams)
	require.True(t, h.admit(a))
	h.release(a)
	for _, slots := range []int{1, 3, 4} {
		h = New(nil, slots, nil)
		for i := 0; i < max(1, slots/2); i++ {
			c := a
			c.AssignmentID = string(rune('a' + i))
			require.True(t, h.admit(c))
		}
		a.AssignmentID = "excess"
		require.False(t, h.admit(a))
	}
}

func TestDownloadRefusalReleasesIdentity(t *testing.T) {
	e := newEnv(t, 4)
	e.artifact(t, "acme", "fw-1", content(1000))
	v, minted := e.grant(t, "acme", "dev-1", "fw-1")
	_, err := e.api.ApplyReport(ctxOf("acme"), "dev-1", ota.Report{V: 1, Kind: ota.KindProgress,
		AttemptID: v.Attempt.Token, AssignmentID: v.Assignment.Token, Component: "firmware",
		ArtifactDigest: v.Attempt.TargetDigest, Seq: 1, Stage: ota.StageDownloading, BootID: "b"})
	require.NoError(t, err)
	_, err = e.api.CancelUpdate(ctxOf("acme"), v.Assignment.Token)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		resp, _ := get(t, e.rebase(t, minted))
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	}
	require.Empty(t, e.h.streams)
	require.Empty(t, e.h.tenants)
}
