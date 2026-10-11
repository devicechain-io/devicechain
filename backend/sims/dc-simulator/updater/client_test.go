// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/stretchr/testify/require"
)

// capture is a Transport that keeps every message the device sends, decoded through the platform
// decoder as it arrives: a device message the contract rejects fails the test at the send.
type capture struct {
	t    *testing.T
	mu   sync.Mutex
	sent []ota.DeviceReport
	raw  [][]byte
	err  error
}

func (c *capture) Send(_ context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	rep, err := ota.DecodeDeviceReport(payload)
	require.NoError(c.t, err, "the device sent something the platform contract rejects: %s", payload)
	c.sent = append(c.sent, rep)
	c.raw = append(c.raw, append([]byte(nil), payload...))
	return nil
}

func (c *capture) stages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.sent {
		switch {
		case r.Update != nil && r.Update.Kind == ota.KindProgress:
			out = append(out, string(r.Update.Stage))
		case r.DownloadRequest != nil:
			out = append(out, "download.request")
		}
	}
	return out
}

func (c *capture) count(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.sent {
		if r.Kind == kind {
			n++
		}
	}
	return n
}

// rangeServer is a stand-in download endpoint: a single object, honouring single ranges, with
// switches for the misbehaviours the device must survive.
type rangeServer struct {
	data []byte

	mu       sync.Mutex
	ranges   []string // every Range header received
	cutAfter int      // when > 0, abort the connection after writing this many body bytes (once)
	status   int      // when set, answer every request with it
	ignore   bool     // answer a ranged request with 200 and the whole object
	corrupt  bool     // flip a byte of every body
	srv      *httptest.Server
}

func newRangeServer(t *testing.T, data []byte) *rangeServer {
	s := &rangeServer{data: data}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rangeServer) url() string { return s.srv.URL + "/updates/download/fw-1?cap=test" }

func (s *rangeServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.ranges = append(s.ranges, r.Header.Get("Range"))
	status, ignore, corrupt, cut := s.status, s.ignore, s.corrupt, s.cutAfter
	if cut > 0 {
		s.cutAfter = 0
	}
	s.mu.Unlock()
	if status != 0 {
		http.Error(w, "refused", status)
		return
	}
	body := s.data
	start, end := int64(0), int64(len(body)-1)
	partial := false
	if spec := r.Header.Get("Range"); spec != "" && !ignore {
		var a, b int64
		if _, err := fmt.Sscanf(spec, "bytes=%d-%d", &a, &b); err != nil || a >= int64(len(body)) {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, end, partial = a, min(b, int64(len(body)-1)), true
	}
	window := append([]byte(nil), body[start:end+1]...)
	if corrupt && len(window) > 0 {
		window[0] ^= 0xff
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(window)))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(http.StatusPartialContent)
	}
	if cut > 0 && cut < len(window) {
		_, _ = w.Write(window[:cut])
		// Flush so the client has the status line and a partial body: a retry-able failure
		// before any response byte would be hidden by net/http replaying the GET.
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	_, _ = w.Write(window)
}

func (s *rangeServer) rangeHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func (s *rangeServer) set(f func(*rangeServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

type clientRig struct {
	t      *testing.T
	cap    *capture
	srv    *rangeServer
	cfg    updater.ClientConfig
	client *updater.Client
	asg    []byte
	now    time.Time
}

const clientChunk = 1000

func newClientRig(t *testing.T) *clientRig {
	t.Helper()
	data := image()
	srv := newRangeServer(t, data)
	r := &clientRig{t: t, cap: &capture{t: t}, srv: srv, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	r.cfg = updater.ClientConfig{
		StatePath: filepath.Join(t.TempDir(), "state.json"), Component: "firmware", InitialVersion: "1.0.0",
		ChunkSize: clientChunk, Transport: r.cap, Now: func() time.Time { return r.now },
	}
	r.client = r.open()
	asg, err := ota.EncodeUpdateAssignment("att-1", "asg-1", "firmware", ota.AssignmentArtifact{
		Version: "2.0.0", Digest: digestOf(data), Size: int64(len(data)), RequiresReboot: true})
	require.NoError(t, err)
	r.asg = asg
	return r
}

func (r *clientRig) open() *updater.Client {
	r.t.Helper()
	c, err := updater.OpenClient(r.cfg)
	require.NoError(r.t, err)
	return c
}

func (r *clientRig) deliver(payload []byte) {
	r.t.Helper()
	require.NoError(r.t, r.client.HandleDesired(context.Background(), payload))
}

func (r *clientRig) grant(ttl time.Duration) {
	r.t.Helper()
	g, err := ota.EncodeDownloadGrant("asg-1", r.srv.url(), r.now.Add(ttl))
	require.NoError(r.t, err)
	r.deliver(g)
}

func TestTheClientAsksForAGrantAndDownloadsWithRanges(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)

	done, err := r.client.Drive(context.Background(), 50)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant, "RECEIVED is reported, then the download needs a grant")
	require.False(t, done)
	require.Equal(t, []string{"RECEIVED", "download.request"}, r.cap.stages())
	// Asking twice before an answer is one request, not a flood.
	_, err = r.client.Drive(context.Background(), 5)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	require.Equal(t, 1, r.cap.count(ota.KindDownloadRequest))

	r.grant(10 * time.Minute)
	done, err = r.client.Drive(context.Background(), 200)
	require.NoError(t, err)
	require.True(t, done)

	// Every window was asked for by range, in order, with no overlap.
	for i, h := range r.srv.rangeHeaders() {
		require.Equal(t, fmt.Sprintf("bytes=%d-%d", i*clientChunk, (i+1)*clientChunk-1), h)
	}
	stages := r.cap.stages()
	require.Equal(t, "RECEIVED", stages[0])
	require.Equal(t, "download.request", stages[1])
	require.Equal(t, []string{"DOWNLOADED", "VERIFIED", "INSTALLING", "REBOOTING", "RUNNING"}, stages[len(stages)-5:])
	snap := r.client.Updater().Snapshot()
	require.Equal(t, "2.0.0", snap.RunningVersion)
	require.Equal(t, "boot-2", snap.BootID)
}

func TestACutDownloadResumesAtThePersistedOffsetAfterARestart(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)
	_, err := r.client.Drive(context.Background(), 3)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	r.grant(10 * time.Minute)

	// Four windows arrive, the fifth is cut mid-body.
	_, err = r.client.Drive(context.Background(), 4)
	require.NoError(t, err)
	r.srv.set(func(s *rangeServer) { s.cutAfter = 400 })
	_, err = r.client.Drive(context.Background(), 5)
	require.Error(t, err)
	require.ErrorContains(t, err, "unexpected EOF")
	have := r.client.Updater().Snapshot().BytesHave
	require.EqualValues(t, 4*clientChunk, have, "a window that did not arrive in full is not committed")

	// The process dies. A new client over the same state file asks for the grant again (it holds
	// none in memory) and resumes at the persisted offset, not at zero.
	r.client = r.open()
	require.Equal(t, have, r.client.Updater().Snapshot().BytesHave)
	before := len(r.srv.rangeHeaders())
	_, err = r.client.Drive(context.Background(), 5)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	r.grant(10 * time.Minute)
	done, err := r.client.Drive(context.Background(), 200)
	require.NoError(t, err)
	require.True(t, done)

	after := r.srv.rangeHeaders()[before:]
	require.Equal(t, fmt.Sprintf("bytes=%d-%d", have, have+clientChunk-1), after[0],
		"the first request after the restart starts where the device stopped")

	// The report sequence continued: no restart from 1 and no gaps.
	var seqs []uint64
	for _, rep := range r.cap.sent {
		if rep.Update != nil {
			seqs = append(seqs, rep.Update.Seq)
		}
	}
	for i := 1; i < len(seqs); i++ {
		require.Equal(t, seqs[i-1]+1, seqs[i], "sequence numbers are contiguous across the restart")
	}
}

func TestAnExpiredGrantIsRenewedNotUsed(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)
	_, err := r.client.Drive(context.Background(), 3)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	r.grant(time.Minute)
	_, err = r.client.Drive(context.Background(), 3)
	require.NoError(t, err)
	requests := len(r.srv.rangeHeaders())

	// The device clock passes the expiry: it makes no request with the dead URL.
	r.now = r.now.Add(2 * time.Minute)
	r.client.ForgetRequest()
	_, err = r.client.Drive(context.Background(), 3)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	require.Equal(t, requests, len(r.srv.rangeHeaders()))
	require.Equal(t, 2, r.cap.count(ota.KindDownloadRequest))

	// The platform refusing the URL (its clock ran ahead) is handled the same way.
	r.grant(time.Hour)
	r.srv.set(func(s *rangeServer) { s.status = http.StatusForbidden })
	_, err = r.client.Drive(context.Background(), 3)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	require.Equal(t, 3, r.cap.count(ota.KindDownloadRequest))
	r.srv.set(func(s *rangeServer) { s.status = 0 })
	r.grant(time.Hour)
	done, err := r.client.Drive(context.Background(), 200)
	require.NoError(t, err)
	require.True(t, done)
}

func TestAServerThatIgnoresRangeCannotCorruptAResume(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)
	_, _ = r.client.Drive(context.Background(), 3)
	r.grant(time.Hour)
	_, err := r.client.Drive(context.Background(), 4)
	require.NoError(t, err)
	have := r.client.Updater().Snapshot().BytesHave
	require.Positive(t, have)

	r.srv.set(func(s *rangeServer) { s.ignore = true })
	_, err = r.client.Drive(context.Background(), 3)
	require.ErrorIs(t, err, updater.ErrRangeIgnored)
	require.Equal(t, have, r.client.Updater().Snapshot().BytesHave, "nothing was spliced in")
}

func TestCorruptBytesFailVerificationAndAreReported(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)
	_, _ = r.client.Drive(context.Background(), 3)
	r.grant(time.Hour)
	r.srv.set(func(s *rangeServer) { s.corrupt = true })
	done, err := r.client.Drive(context.Background(), 200)
	require.NoError(t, err)
	require.True(t, done)
	stages := r.cap.stages()
	require.Equal(t, "FAILED", stages[len(stages)-1])
	last := r.cap.sent[len(r.cap.sent)-1].Update
	require.Equal(t, "VERIFICATION_FAILED", last.Result.Code)
	require.NotContains(t, stages, "VERIFIED")
	require.NotContains(t, stages, "INSTALLING")
	require.Equal(t, "1.0.0", r.client.Updater().Snapshot().RunningVersion, "a failed verification installs nothing")
}

func TestACancelAbandonsBeforeInstallAndIsIgnoredAfter(t *testing.T) {
	r := newClientRig(t)
	r.deliver(r.asg)
	_, _ = r.client.Drive(context.Background(), 3)
	r.grant(time.Hour)
	_, err := r.client.Drive(context.Background(), 3)
	require.NoError(t, err)

	cancel, err := ota.EncodeUpdateCancel("att-1", "asg-1")
	require.NoError(t, err)
	r.deliver(cancel)
	stages := r.cap.stages()
	require.Equal(t, "ABANDONED", stages[len(stages)-1])

	// A cancel for an assignment the device is not working on, and a message of another family,
	// do nothing.
	other, err := ota.EncodeUpdateCancel("", "asg-other")
	require.NoError(t, err)
	n := len(r.cap.sent)
	r.deliver(other)
	require.Len(t, r.cap.sent, n)
	err = r.client.HandleDesired(context.Background(), []byte(`{"v":1,"kind":"config.revision","revision":1,"digest":"sha256:`+strings.Repeat("a", 64)+`","document":"{}"}`))
	require.ErrorIs(t, err, updater.ErrNotUpdateKind)

	// Past the point of no return, a cancel is refused by the device: it is installing.
	r2 := newClientRig(t)
	r2.deliver(r2.asg)
	_, _ = r2.client.Drive(context.Background(), 3)
	r2.grant(time.Hour)
	for r2.client.Updater().Snapshot().Stage != "INSTALLING" {
		_, err = r2.client.Drive(context.Background(), 1)
		require.NoError(t, err)
	}
	r2.deliver(cancel)
	st := r2.cap.stages()
	require.Equal(t, "INSTALLING", st[len(st)-1], "no ABANDONED once installing")
}

func TestTheClientRejectsMalformedDesiredMessages(t *testing.T) {
	r := newClientRig(t)
	for name, body := range map[string]string{
		"not json":      `nope`,
		"unknown field": `{"v":1,"kind":"update.cancel","assignmentId":"asg-1","extra":true}`,
		"trailing":      `{"v":1,"kind":"update.cancel","assignmentId":"asg-1"} {}`,
		"bad expiry":    `{"v":1,"kind":"download.grant","assignmentId":"asg-1","url":"http://x/","expiresAt":"soon"}`,
	} {
		require.Error(t, r.client.HandleDesired(context.Background(), []byte(body)), name)
	}
}

// The device speaks the wire from its own structs; the platform golden fixtures are what hold the
// two together. Every desired fixture must be accepted by the device, and every message the device
// emits must be accepted by the platform decoder (the capture transport already enforces that).
func TestTheDeviceAcceptsEveryDesiredFixture(t *testing.T) {
	r := newClientRig(t)
	fixture := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "core", "ota", "testdata", "update", "desired", name+".json"))
		require.NoError(t, err)
		return raw
	}
	require.NoError(t, r.client.HandleDesired(context.Background(), fixture("update-assignment")))
	// The fixture assignment is att-0001 / asg-0001; the device took it.
	require.Equal(t, "att-0001", r.client.Updater().Snapshot().AttemptID)
	// The fixture grant expires at the instant the device clock reads, so it is already dead and
	// the device asks again.
	require.NoError(t, r.client.HandleDesired(context.Background(), fixture("download-grant")))

	// And the one request it makes is the platform fixture, byte for byte.
	_, err := r.client.Drive(context.Background(), 5)
	require.ErrorIs(t, err, updater.ErrAwaitingGrant)
	require.NoError(t, r.client.HandleDesired(context.Background(), fixture("update-cancel")))
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "core", "ota", "testdata", "update", "valid", "download-request.json"))
	require.NoError(t, err)
	var got string
	for _, raw := range r.cap.raw {
		if strings.Contains(string(raw), `"download.request"`) {
			got = string(raw)
		}
	}
	require.JSONEq(t, string(want), got)
	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &obj))
}
