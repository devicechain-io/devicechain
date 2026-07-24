// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build interop

// This is the Eclipse Leshan LwM2M interop conformance harness (edge-validation slice 1). Unlike
// integration_test.go — which drives the /rd path with OUR OWN pion/go-coap client, so a bug shared
// by our encoder and our decoder would cancel — this file drives the REAL server with an independent
// Java implementation (Eclipse Leshan). It is tag-gated (`//go:build interop`): plain `go test ./...`
// never compiles it and never needs a JVM; only `go test -tags interop` (the periodic lwm2m-interop
// workflow, with Java + the built client jar present) runs it.
//
// Crucially it does NOT reuse startIntegrationOpts, which passes a nil observer (integration_test.go)
// and so exercises presence only — the SenML decode and CoRE-link decode paths never run there. This
// harness wires the FULL telemetry path exactly like main.go: a real observe.Manager over a
// *recording ingester*, so Leshan's SenML/CoRE-link output really flows through decode.Samples /
// decode.Observations before anything is asserted. That is what makes the encoder/decoder-cancel
// blind spot impossible to hide in.

package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/decode"
	"github.com/devicechain-io/dc-lwm2m-ingest/downlink"
	"github.com/devicechain-io/dc-lwm2m-ingest/observe"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
)

// interopSentinel prefixes every structured status line the Leshan client prints on stdout, so the
// harness can pull them out of a stream that also carries the JVM's own output. All SLF4J/Californium
// logging is forced to stderr in the client jar, so stdout is the clean correlation channel.
const interopSentinel = "DCINTEROP "

// leshanStatus is one status line the client emits (JSON after the sentinel). It is used for
// diagnostics in failure messages; the load-bearing assertions read the server-side recording seams
// (the fake presence emitter and the recording ingester), never the client's self-report — a client
// that lies or no-ops cannot manufacture a pass.
type leshanStatus struct {
	Scenario string `json:"scenario"`
	Event    string `json:"event"`
	RegID    string `json:"regId,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// recordingIngester captures the decoded samples that reach the observe ingest seam. It satisfies
// the (unexported) observe.ingester interface structurally, so a Notify travels the REAL path —
// go-coap read loop → decode.Samples on Leshan's SenML bytes → here — before it is recorded. A decode
// bug therefore cannot cancel against an encoder bug: the encoder is Leshan's, not ours.
type recordingIngester struct {
	mu      sync.Mutex
	batches [][]adapter.Sample
}

func (r *recordingIngester) Ingest(_ context.Context, _ string, _ adapter.IngestPolicy, _ string, samples []adapter.Sample) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, append([]adapter.Sample(nil), samples...))
	return nil
}

// samples flattens every recorded batch into one slice (order preserved).
func (r *recordingIngester) samples() []adapter.Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []adapter.Sample
	for _, b := range r.batches {
		out = append(out, b...)
	}
	return out
}

// batchCount is the number of Notifies decoded — one batch per Ingest call. ≥2 proves an
// established observation delivered a steady-state Notify, not just the one-shot initial response.
func (r *recordingIngester) batchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// obsMetrics are real counters wired into the observe.Manager so a failing observe scenario can
// report WHY (a refused Observe, an undecodable Notify, an unknown content format) instead of a bare
// timeout — a failed Block2 reassembly is otherwise indistinguishable from "the client never sent it".
type obsMetrics struct {
	notifies, decodeFail, unknownCF, refused, terminal, truncated, dropped prometheus.Counter
}

func newObsMetrics() *obsMetrics {
	c := func(n string) prometheus.Counter { return prometheus.NewCounter(prometheus.CounterOpts{Name: n}) }
	return &obsMetrics{c("notifies"), c("decode_fail"), c("unknown_cf"), c("refused"), c("terminal"), c("truncated"), c("dropped")}
}

func (m *obsMetrics) manager() observe.Metrics {
	return observe.Metrics{
		NotifiesReceived:        m.notifies,
		DecodeFailures:          m.decodeFail,
		UnknownContentFormat:    m.unknownCF,
		ObserveEstablishRefused: m.refused,
		TerminalNotifications:   m.terminal,
		SamplesTruncated:        m.truncated,
		IngestDropped:           m.dropped,
	}
}

func (m *obsMetrics) dump() string {
	return fmt.Sprintf("observe metrics: notifies=%.0f decodeFail=%.0f unknownCF=%.0f observeRefused=%.0f terminal=%.0f truncated=%.0f ingestDropped=%.0f",
		testutil.ToFloat64(m.notifies), testutil.ToFloat64(m.decodeFail), testutil.ToFloat64(m.unknownCF),
		testutil.ToFloat64(m.refused), testutil.ToFloat64(m.terminal), testutil.ToFloat64(m.truncated), testutil.ToFloat64(m.dropped))
}

// interopHarness is one in-process server plus the recording seams a scenario asserts against.
type interopHarness struct {
	addr string
	res  *fakeResolver
	em   *fakeEmitter
	ing  *recordingIngester
	om   *obsMetrics
}

// startInteropServer stands up a real DTLS-PSK CoAP server with the /rd handlers over a real
// Registry AND a real observe.Manager, wired like main.go's afterMicroserviceInitialized: the
// manager's ingest seam is a recording ingester and OnSessionEnd fans a session end into
// obsMgr.Cancel + connTable.End, exactly as production does. opts lets a scenario tune the lifetime
// clamps (the lifetime-lapse scenario needs seconds, not the 60s/30s package defaults).
func startInteropServer(t *testing.T, opts Options) *interopHarness {
	t.Helper()
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveCreated}
	em := &fakeEmitter{}
	ing := &recordingIngester{}
	om := newObsMetrics()
	obsMgr := observe.NewManager(ing, nil /*ungated*/, om.manager(), observe.Options{})
	connTable := downlink.NewConnTable()

	if opts.Source == "" {
		opts.Source = SourceLwM2M
	}
	prevEnd := opts.OnSessionEnd
	opts.OnSessionEnd = func(identity string, epoch uint64) {
		obsMgr.Cancel(identity, epoch)
		connTable.End(identity, epoch)
		if prevEnd != nil {
			prevEnd(identity, epoch)
		}
	}
	reg := New(res, em, adapter.NewEpochSource(nil), Metrics{}, opts)
	h := NewHandlers(reg, itBindings, Metrics{}, obsMgr, connTable, decode.DefaultObjectAllowlist, nil /*ungated*/, nil /*always-serving*/)

	srv, err := server.New(server.Config{
		Addr:        "127.0.0.1:0",
		Credentials: map[string][]byte{"dev-1": itPSK, "dev-2": itPSK},
		CIDLength:   8,
		MaxSessions: 1000,
	}, server.Metrics{}, h.Mount)
	require.NoError(t, err)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Stop)
	return &interopHarness{addr: srv.Addr().String(), res: res, em: em, ing: ing, om: om}
}

// emDisconnects returns the recorded presence writes that marked the device offline.
func emDisconnects(em *fakeEmitter) []emitted {
	var out []emitted
	for _, e := range em.all() {
		if !e.ev.Connected {
			out = append(out, e)
		}
	}
	return out
}

// --- the Leshan subprocess -------------------------------------------------

// syncBuffer is an io.Writer safe for the concurrent writes go-exec makes from its copy goroutine
// while the test reads the accumulated output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testLogWriter streams the client's stderr (its logs) into the test log so a failing run is
// diagnosable from the CI output.
type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Logf("[leshan] %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// leshanProc is a running Leshan client. Some scenarios self-terminate (lifecycle); others are held
// open by the harness and killed once the server-side signal has been observed (observe, lifetime
// lapse). Either way the whole process GROUP is killed on cleanup so a hung JVM can neither orphan
// nor block Wait.
type leshanProc struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	t      *testing.T
	once   sync.Once
}

// leshanJar resolves the client jar. It is the anti-vacuous floor: with the jar absent it SKIPS
// locally (so `go test -tags interop` is not a landmine on a dev box without the build) but FAILS in
// CI (CI is set), so the release-adjacent workflow can never pass by silently running nothing.
func leshanJar(t *testing.T) string {
	jar := os.Getenv("DC_LESHAN_JAR")
	if jar == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("DC_LESHAN_JAR is required in CI: the Leshan interop client jar must be built and its path exported, or the suite would pass vacuously")
		}
		t.Skip("DC_LESHAN_JAR unset — skipping Leshan interop. Build backend/services/lwm2m-ingest/interop/leshan-client (mvn package) and export DC_LESHAN_JAR to run.")
	}
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("DC_LESHAN_JAR=%q is not a readable file: %v", jar, err)
	}
	return jar
}

// startLeshan launches the client for one scenario in its own process group.
func startLeshan(t *testing.T, jar, addr, identity, scenario string, extra ...string) *leshanProc {
	t.Helper()
	args := append([]string{
		"-jar", jar,
		"--server", addr,
		"--identity", identity,
		"--psk-b64", base64.StdEncoding.EncodeToString(itPSK),
		"--scenario", scenario,
	}, extra...)
	cmd := exec.Command("java", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = testLogWriter{t}
	require.NoError(t, cmd.Start(), "launch Leshan client")
	lp := &leshanProc{cmd: cmd, stdout: out, t: t}
	t.Cleanup(lp.kill)
	return lp
}

// kill terminates the whole process group and reaps it. Safe to call more than once.
func (lp *leshanProc) kill() {
	lp.once.Do(func() {
		// Only signal the process group while it is still running — after Wait() reaps the JVM its
		// pid/pgid can be recycled, and killing a recycled pgid could hit an unrelated process.
		if lp.cmd.Process != nil && lp.cmd.ProcessState == nil {
			_ = syscall.Kill(-lp.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = lp.cmd.Wait()
	})
}

// wait blocks until a self-terminating scenario exits (bounded by the caller's use of waitFor before
// it, plus WaitDelay). It does not assert the exit code: a scenario the harness kills mid-run exits
// non-zero by design, and the verdict is read from the server-side seams, not the client.
func (lp *leshanProc) wait() { _ = lp.cmd.Wait() }

// statuses parses the sentinel-prefixed JSON lines the client printed (diagnostics only).
func (lp *leshanProc) statuses() []leshanStatus {
	var out []leshanStatus
	for _, line := range strings.Split(lp.stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, interopSentinel) {
			continue
		}
		var s leshanStatus
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, interopSentinel)), &s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// waitFor polls cond until it holds or the deadline passes, failing the test on timeout with msg,
// an optional diagnostic (e.g. the observe metrics), and the client's self-reported statuses.
func waitFor(t *testing.T, lp *leshanProc, timeout time.Duration, cond func() bool, msg string, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	extra := ""
	if diag != nil {
		extra = "; " + diag()
	}
	t.Fatalf("timed out after %s waiting for %s%s; client statuses=%+v", timeout, msg, extra, lp.statuses())
}

// --- the scenarios ---------------------------------------------------------

func TestLeshanInterop(t *testing.T) {
	jar := leshanJar(t)

	// Register → Update → Deregister with a hostile `ep`, all from a real Leshan client. Proves the
	// CoAP registration interface, DTLS-PSK on the server's hard-pinned CCM_8 suite, the presence
	// lifecycle, AND D1 tenancy recovery (tenant/externalId from the credential, never the payload).
	t.Run("lifecycle_and_tenancy", func(t *testing.T) {
		h := startInteropServer(t, Options{})
		// Authenticate as dev-1 (binding acme / plant-a/sensor-1) but assert a HOSTILE registration ep
		// claiming to be another tenant's device (beta / plant-b/sensor-9). The server must bind tenancy
		// from the authenticated PSK identity, never the payload ep — the D1 red line.
		lp := startLeshan(t, jar, h.addr, "dev-1", "lifecycle", "--endpoint", "beta/plant-b/sensor-9")
		// The client self-terminates after deregister; bound the wait on the server-side signals.
		waitFor(t, lp, 30*time.Second, func() bool { return len(h.em.connects()) >= 1 }, "CONNECTED from Register", nil)
		waitFor(t, lp, 30*time.Second, func() bool { return len(emDisconnects(h.em)) >= 1 }, "DISCONNECTED from Deregister", nil)
		lp.wait()

		require.Len(t, h.em.connects(), 1, "exactly one CONNECTED edge from one Register")
		// Tenancy came from dev-1's binding (acme / plant-a/sensor-1), NOT the hostile ep the client
		// asserted in its registration — the D1 red line, now proven against a foreign client.
		require.Equal(t, "acme", h.em.connects()[0].tenant, "tenant from credential binding")
		require.Equal(t, "plant-a/sensor-1", h.res.lastCall().externalId, "externalId from credential binding, not payload ep")
	})

	// Observe/Notify: the client hosts an IPSO Temperature (3303) instance and, after registration,
	// changes the value three times (22.5/23.5/24.5) — each a real SenML Notify. Requiring ≥2 recorded
	// batches with distinct values proves an observation was ESTABLISHED and its steady-state Notify
	// stream decoded — not the one-shot initial response (which the obs.Canceled() path records even
	// when observation is broken). The value round-trips through Leshan's SenML encoder and OUR decoder.
	t.Run("observe_notify", func(t *testing.T) {
		h := startInteropServer(t, Options{})
		lp := startLeshan(t, jar, h.addr, "dev-1", "observe", "--value", "21.5")
		waitFor(t, lp, 30*time.Second, func() bool { return h.ing.batchCount() >= 2 },
			"≥2 decoded Leshan Notifies (an established observation, not a one-shot read)", h.om.dump)

		values := map[float64]bool{}
		sawInitial := false
		for _, s := range h.ing.samples() {
			if s.Name == "/3303/0/5700" {
				values[s.Value] = true
				if s.Value == 21.5 {
					sawInitial = true
				}
			}
		}
		require.Truef(t, sawInitial, "expected the initial /3303/0/5700=21.5 sample decoded from Leshan's SenML; got %+v", h.ing.samples())
		require.GreaterOrEqualf(t, len(values), 2, "expected ≥2 distinct decoded values (a real Notify stream); got %v", values)
		// A clean exchange: the observation established (not refused/one-shot), nothing terminal, nothing undecodable.
		require.Zerof(t, testutil.ToFloat64(h.om.refused), "observation must be established, not refused/one-shot; %s", h.om.dump())
		require.Zerof(t, testutil.ToFloat64(h.om.terminal), "no terminal notification; %s", h.om.dump())
		require.Zerof(t, testutil.ToFloat64(h.om.decodeFail), "no decode failures; %s", h.om.dump())
	})

	// Block2: the client hosts a custom object (3441, top of the IPSO allowlist range) with 100 numeric
	// resources and forces a small CoAP block size, so reading /3441/0 is a ~2KB SenML pack split into
	// ≥8 Block2 blocks. It also fires a change so a blockwise NOTIFICATION is reassembled, not just the
	// initial response. Proves go-coap reassembles a Leshan-fragmented notify and our decoder reads the
	// whole pack with correct content. 100 < the 256 MaxSamplesPerNotify cap, so nothing is truncated.
	t.Run("observe_block2", func(t *testing.T) {
		const n = 100
		h := startInteropServer(t, Options{})
		lp := startLeshan(t, jar, h.addr, "dev-1", "observe-block2", "--count", "100")
		waitFor(t, lp, 40*time.Second, func() bool { return h.ing.batchCount() >= 2 },
			"≥2 Block2-reassembled notifies decoded whole", h.om.dump)

		// Every resource must be present with its exact value (57NN == NN) — a reassembly bug that
		// dropped or scrambled a block fails here, not just a bare count.
		latest := map[string]float64{}
		for _, s := range h.ing.samples() {
			latest[s.Name] = s.Value
		}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("/3441/0/%d", 5700+i)
			v, ok := latest[name]
			require.Truef(t, ok, "missing reassembled sample %s (Block2 dropped a block?); %s", name, h.om.dump())
			require.Equalf(t, float64(i), v, "wrong reassembled value at %s", name)
		}
		require.Zerof(t, testutil.ToFloat64(h.om.refused), "observation must be established, not refused/one-shot; %s", h.om.dump())
		require.Zerof(t, testutil.ToFloat64(h.om.decodeFail), "no decode failures on the reassembled pack; %s", h.om.dump())
	})

	// Lifetime lapse: register with short server-side lifetime clamps, then KILL the client so no
	// Update refreshes it, and assert the server marks the device offline on lifetime expiry (not on
	// an explicit Deregister). This is the authoritative-death path a Connectivity DETECT rule fires on.
	t.Run("lifetime_lapse", func(t *testing.T) {
		h := startInteropServer(t, Options{MinLifetime: 2 * time.Second, MaxLife: 3 * time.Second, Grace: 1 * time.Second})
		lp := startLeshan(t, jar, h.addr, "dev-1", "register-hold")
		waitFor(t, lp, 30*time.Second, func() bool { return len(h.em.connects()) >= 1 }, "CONNECTED from Register", nil)
		lp.kill() // stop the client: no more Updates, so the lifetime must lapse
		waitFor(t, lp, 30*time.Second, func() bool { return len(emDisconnects(h.em)) >= 1 }, "DISCONNECTED from lifetime expiry (no Deregister)", nil)
	})
}
