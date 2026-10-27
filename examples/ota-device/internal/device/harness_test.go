// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package device_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/boot"
	"devicechain.io/examples/ota-device/internal/device"
	"devicechain.io/examples/ota-device/internal/testutil"
	"devicechain.io/examples/ota-device/internal/verify"
	"devicechain.io/examples/ota-device/internal/wire"
)

const (
	product    = "ota-device-example"
	capSecret  = "TOPSECRETCAP"
	downloadAt = "/api/update-management/updates/download/"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// recorder is the device-reports topic: every frame the agent publishes lands here, and every one
// is checked against the platform's own decoder as it is read back.
type recorder struct {
	mu     sync.Mutex
	frames [][]byte
	failOn string // when set, a frame containing this text fails to send once
}

func (r *recorder) Send(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn != "" && contains(b, r.failOn) {
		r.failOn = ""
		return errors.New("link down")
	}
	r.frames = append(r.frames, append([]byte(nil), b...))
	return nil
}

func contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

func (r *recorder) all() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.frames...)
}

func (r *recorder) kinds(t *testing.T) []string {
	var out []string
	for _, f := range r.all() {
		k, err := wire.CheckOutbound(f)
		require.NoError(t, err, "%s", f)
		out = append(out, k)
	}
	return out
}

func (r *recorder) count(t *testing.T, kind string) int {
	n := 0
	for _, k := range r.kinds(t) {
		if k == kind {
			n++
		}
	}
	return n
}

// progress returns every update.progress, each also decoded by the platform's own decoder.
func (r *recorder) progress(t *testing.T) []ota.Report {
	var out []ota.Report
	for _, f := range r.all() {
		var probe struct{ Kind string }
		require.NoError(t, json.Unmarshal(f, &probe))
		if probe.Kind != wire.KindProgress && probe.Kind != wire.KindInventory {
			continue
		}
		rep, err := ota.DecodeReport(f)
		require.NoError(t, err, "the platform must accept what this device sent: %s", f)
		if probe.Kind == wire.KindProgress {
			out = append(out, rep)
		}
	}
	return out
}

func (r *recorder) stages(t *testing.T) []string {
	var out []string
	for _, p := range r.progress(t) {
		out = append(out, string(p.Stage))
	}
	return out
}

func (r *recorder) failure(t *testing.T) (string, string, bool) {
	for _, p := range r.progress(t) {
		if p.Stage == ota.StageFailed {
			return p.Result.Code, p.Result.Detail, true
		}
	}
	return "", "", false
}

// image is a firmware image as the platform would hold it.
type image struct {
	name    string
	version string
	bytes   []byte
	digest  string
}

func mkImage(t *testing.T, name, version, prod, mode string) image {
	t.Helper()
	b, err := os.ReadFile(testutil.Fake(t, version, prod, mode))
	require.NoError(t, err)
	d := verifyDigest(b)
	return image{name: name, version: version, bytes: b, digest: d}
}

func verifyDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type env struct {
	t      *testing.T
	root   string
	signer *testutil.Signer
	trust  *verify.TrustSet
	srv    *httptest.Server
	rec    *recorder
	agent  *device.Agent

	mu       sync.Mutex
	served   map[string][]byte // download name -> bytes the server will return
	requests []string          // Range header of every download request
	cutFirst map[string]int    // download name -> cut the first response after this many bytes
	status   map[string]int    // download name -> force this status
	rebooted bool
	seq      int
	version  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, root: t.TempDir(), signer: testutil.NewECDSA(), rec: &recorder{},
		served: map[string][]byte{}, cutFirst: map[string]int{}, status: map[string]int{}, version: "1.0.0"}
	var err error
	e.trust, err = verify.ParseTrust(e.signer.PEM)
	require.NoError(t, err)

	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)

	// The device starts out running 1.0.0 from slot a.
	base := mkImage(t, "base", "1.0.0", product, "good")
	e.installImage("a", base.bytes)
	require.NoError(t, boot.Save(e.root, boot.State{Confirmed: "a"}))
	e.agent = e.newAgent("a", "1.0.0", "boot-1")
	return e
}

func (e *env) installImage(slot string, b []byte) {
	require.NoError(e.t, os.MkdirAll(boot.SlotDir(e.root, slot), 0o700))
	require.NoError(e.t, os.WriteFile(boot.ImagePath(e.root, slot), b, 0o700))
}

func (e *env) origin() *url.URL {
	u, err := url.Parse(e.srv.URL)
	require.NoError(e.t, err)
	return u
}

func (e *env) newAgent(slot, version, bootID string) *device.Agent {
	e.version = version
	a, err := device.New(device.Config{
		Root: e.root, Slot: slot, Component: "firmware", Product: product, Version: version, BootID: bootID,
		Trust: e.trust, RequireSigned: true, Origin: e.origin(), AllowInsecure: true, HTTP: e.srv.Client(),
		SelfPath: boot.ImagePath(e.root, slot), GrantAttempts: 3, ProgressEvery: time.Hour,
		Reboot: func() { e.mu.Lock(); e.rebooted = true; e.mu.Unlock() },
		Log:    func(f string, a ...any) { e.t.Logf("device: "+f, a...) },
	}, e.rec)
	require.NoError(e.t, err)
	return a
}

func (e *env) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	name := r.URL.Path[len(downloadAt):]
	body, ok := e.served[name]
	cut, doCut := e.cutFirst[name]
	if doCut {
		delete(e.cutFirst, name)
	}
	forced := e.status[name]
	e.requests = append(e.requests, r.Header.Get("Range"))
	e.mu.Unlock()
	if r.URL.Query().Get("cap") != capSecret || !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if forced != 0 {
		w.WriteHeader(forced)
		return
	}
	if doCut {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:cut])
		w.(http.Flusher).Flush()
		hj, _, _ := w.(http.Hijacker).Hijack()
		_ = hj.Close()
		return
	}
	http.ServeContent(w, r, name, time.Unix(1_700_000_000, 0), bytes.NewReader(body))
}

func (e *env) ranges() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

func (e *env) didReboot() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rebooted
}

// publish registers the bytes the server will return for img (which may differ from img.bytes, to
// plant corruption) and returns the assignment the platform would send for img, signed by signer.
func (e *env) publish(img image, served []byte, signer *testutil.Signer, mod func(*wire.Assignment)) wire.Assignment {
	e.mu.Lock()
	e.served[img.name] = served
	e.seq++
	n := e.seq
	e.mu.Unlock()
	a := wire.Assignment{
		V: 1, Kind: wire.KindAssignment, AttemptID: fmt.Sprintf("att-%d", n), AssignmentID: fmt.Sprintf("asg-%d", n),
		Component: "firmware", Trust: wire.TrustUnverified,
		Artifact: wire.Artifact{Version: img.version, Digest: img.digest, Size: int64(len(img.bytes)), RequiresReboot: true},
	}
	if signer != nil {
		a.Trust = wire.TrustSigned
		a.Signature = signer.Sign(img.digest)
		a.KeyID = signer.KeyID
		a.Algorithm = signer.Algorithm
	}
	if mod != nil {
		mod(&a)
	}
	return a
}

func (e *env) deliver(v any) {
	b, err := json.Marshal(v)
	require.NoError(e.t, err)
	e.agent.Handle(context.Background(), b)
}

func (e *env) grantFor(a wire.Assignment, name string) wire.Grant {
	return wire.Grant{V: 1, Kind: wire.KindGrant, AssignmentID: a.AssignmentID,
		URL:       e.srv.URL + downloadAt + name + "?cap=" + capSecret,
		ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)}
}

// run delivers the assignment and then one grant, as the platform would on the device's ask.
func (e *env) run(a wire.Assignment, name string) {
	e.deliver(a)
	if e.rec.count(e.t, wire.KindDownloadRequest) == 0 {
		return
	}
	e.deliver(e.grantFor(a, name))
}

// marker reads what the image in the inactive slot recorded about being run, or "" if it never ran.
func (e *env) marker(slot string) string {
	b, err := os.ReadFile(filepath.Join(boot.SlotDir(e.root, slot), "ran.marker"))
	if err != nil {
		return ""
	}
	return string(b)
}

func (e *env) bootState() boot.State {
	s, _, err := boot.Load(e.root)
	require.NoError(e.t, err)
	return s
}

func (e *env) hasImage(slot string) bool {
	_, err := os.Stat(boot.ImagePath(e.root, slot))
	return err == nil
}
