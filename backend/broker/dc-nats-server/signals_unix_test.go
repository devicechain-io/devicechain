// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dc-broker/nats-server/internal/smoke"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// The tests in this file run the BUILT BINARY, because what they test is what only the binary has:
// the signal loop, the command line, and the `-sl` path the chart's preStop hook uses.

// binary builds this package once per test binary run.
var builtBinary string

func buildBinary(t *testing.T) string {
	t.Helper()
	if builtBinary != "" {
		return builtBinary
	}
	dir, err := os.MkdirTemp("", "dc-nats-server-bin-")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "nats-server")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	builtBinary = bin
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type proc struct {
	bin, dir, pid string
	cmd           *exec.Cmd
	mqtt, nats    string
	metrics, log  string
	done          chan error
}

// fixture is a prepared config directory on free ports, which a test may start more than once.
type fixture struct {
	dir, pid                     string
	client, mqtt, http, metricsP int
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{dir: t.TempDir(), client: freePort(t), mqtt: freePort(t), http: freePort(t), metricsP: freePort(t)}
	f.pid = filepath.Join(f.dir, "nats.pid")
	if err := smoke.Prepare(f.dir, smoke.PrepareOptions{
		PidFile: f.pid, StoreDir: filepath.Join(f.dir, "js"),
		ClientPort: f.client, MQTTPort: f.mqtt, HTTPPort: f.http,
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// startBinary runs the built binary against a fresh smoke config and waits for its first probe.
func startBinary(t *testing.T) *proc {
	t.Helper()
	p := launch(t, newFixture(t), "server.log")
	p.waitMetric(t, "dc_broker_mqtt_client_auth_probes_total", func(v int) bool { return v >= 1 })
	return p
}

// launch starts the binary on f, logging to logName in its directory, and returns at once.
func launch(t *testing.T, f fixture, logName string) *proc {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-c", filepath.Join(f.dir, "nats.conf"))
	cmd.Env = append(os.Environ(), metricsAddrEnv+"=127.0.0.1:"+strconv.Itoa(f.metricsP))
	logPath := filepath.Join(f.dir, logName)
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{
		bin: bin, dir: f.dir, pid: f.pid, cmd: cmd, done: make(chan error, 1), log: logPath,
		mqtt:    net.JoinHostPort("127.0.0.1", strconv.Itoa(f.mqtt)),
		nats:    "tls://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(f.client)),
		metrics: "http://127.0.0.1:" + strconv.Itoa(f.metricsP) + "/metrics",
	}
	go func() { p.done <- cmd.Wait(); logf.Close() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("server log (%s):\n%s", logName, b)
		}
	})
	return p
}

// metric reads one unlabelled series from the binary's own endpoint.
func (p *proc) metric(t *testing.T, name string) (int, bool) {
	t.Helper()
	resp, err := http.Get(p.metrics)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == name {
			v, err := strconv.Atoi(f[1])
			return v, err == nil
		}
	}
	return 0, false
}

func (p *proc) waitMetric(t *testing.T, name string, ok func(int) bool) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if v, found := p.metric(t, name); found && ok(v) {
			return v
		}
		if time.Now().After(deadline) {
			v, found := p.metric(t, name)
			t.Fatalf("%s never reached the expected value (last %d, found=%v)", name, v, found)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *proc) requested(t *testing.T) bool {
	t.Helper()
	req, err := smoke.Probe(p.dir, p.mqtt)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return req
}

func (p *proc) alive() bool {
	select {
	case err := <-p.done:
		p.done <- err
		return false
	default:
		return true
	}
}

func (p *proc) waitExit(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-p.done:
		p.done <- err
		return err
	case <-time.After(within):
		t.Fatalf("the server did not exit within %v", within)
		return nil
	}
}

// TestSIGHUPKeepsClientCertificateRequest: a real SIGHUP, and the `-sl=reload` the chart's
// config-reloader is equivalent to, both leave the listener requesting certificates, and the
// binary's own gauge re-checks after each.
func TestSIGHUPKeepsClientCertificateRequest(t *testing.T) {
	p := startBinary(t)
	if !p.requested(t) {
		t.Fatal("the MQTT listener sent no CertificateRequest at start")
	}
	if v, _ := p.metric(t, "dc_broker_mqtt_client_auth_request"); v != 1 {
		t.Fatalf("dc_broker_mqtt_client_auth_request = %d at start, want 1", v)
	}

	probes, _ := p.metric(t, "dc_broker_mqtt_client_auth_probes_total")
	if err := p.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	probes = p.waitMetric(t, "dc_broker_mqtt_client_auth_probes_total", func(v int) bool { return v > probes })
	if !p.requested(t) {
		t.Fatal("after SIGHUP the MQTT listener sent no CertificateRequest")
	}
	if v, _ := p.metric(t, "dc_broker_mqtt_client_auth_request"); v != 1 {
		t.Fatalf("dc_broker_mqtt_client_auth_request = %d after SIGHUP, want 1", v)
	}

	if out, err := exec.Command(p.bin, "-sl=reload="+p.pid).CombinedOutput(); err != nil {
		t.Fatalf("-sl=reload: %v\n%s", err, out)
	}
	p.waitMetric(t, "dc_broker_mqtt_client_auth_probes_total", func(v int) bool { return v > probes })
	if !p.requested(t) {
		t.Fatal("after -sl=reload the MQTT listener sent no CertificateRequest")
	}
	log, _ := os.ReadFile(p.log)
	if n := strings.Count(string(log), "Reloaded server configuration"); n != 2 {
		t.Fatalf("the server log records %d reloads, want 2 — a reload that did not happen proves nothing", n)
	}
}

// TestLameDuckThroughPreStopIgnoresSIGTERM is upstream's lame duck contract, through the command
// the chart's preStop hook runs: `-sl=ldm=<pid file>` starts a lame duck shutdown, a SIGTERM that
// arrives during it is ignored (Kubernetes sends one when preStop returns), and the server exits 0
// once the lame duck period has closed its clients.
func TestLameDuckThroughPreStopIgnoresSIGTERM(t *testing.T) {
	p := startBinary(t)
	// One client held open, so the lame duck period has a client to close and lasts about
	// lame_duck_grace_period (1s) plus one close interval.
	sec, err := smoke.LoadSecrets(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := smoke.ConnectService(p.dir, p.nats, sec)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	closed := make(chan struct{})
	nc.SetClosedHandler(func(*nats.Conn) { close(closed) })
	nc.SetReconnectHandler(func(*nats.Conn) {})

	if out, err := exec.Command(p.bin, "-sl=ldm="+p.pid).CombinedOutput(); err != nil {
		t.Fatalf("-sl=ldm: %v\n%s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if !p.alive() {
		t.Fatal("the server exited on SIGTERM during lame duck mode; upstream ignores it")
	}
	if err := p.waitExit(t, 20*time.Second); err != nil {
		t.Fatalf("the server exited with %v after lame duck mode, want 0", err)
	}
	log, _ := os.ReadFile(p.log)
	if !strings.Contains(string(log), "Entering lame duck mode") {
		t.Fatal("the server log does not record lame duck mode")
	}
	_ = io.Discard
}

// TestSIGTERMWithoutLameDuckExits: outside lame duck mode, SIGTERM is a prompt, clean shutdown.
func TestSIGTERMWithoutLameDuckExits(t *testing.T) {
	p := startBinary(t)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("exit after SIGTERM = %v, want 0", err)
	}
}

// TestSIGINTExits: SIGINT shuts down and exits 0, as upstream.
func TestSIGINTExits(t *testing.T) {
	p := startBinary(t)
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("exit after SIGINT = %v, want 0", err)
	}
}

// TestCheckConfigFlag: `-t` validates and exits 0, as upstream, without starting anything.
func TestCheckConfigFlag(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	if err := smoke.Prepare(dir, smoke.PrepareOptions{PidFile: filepath.Join(dir, "p"), StoreDir: filepath.Join(dir, "js")}); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-c", filepath.Join(dir, "nats.conf")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "is valid") {
		t.Fatalf("-t: %v\n%s", err, out)
	}
}

// slowStartStore writes a JetStream store whose recovery makes the next start take seconds:
// streams streams, each recovered one by one inside Server.Start, before Run returns.
func slowStartStore(t *testing.T, f fixture, streams int) {
	t.Helper()
	p := launch(t, f, "seed.log")
	p.waitMetric(t, "dc_broker_mqtt_client_auth_probes_total", func(v int) bool { return v >= 1 })
	sec, err := smoke.LoadSecrets(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := smoke.ConnectService(f.dir, p.nats, sec)
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < streams; i++ {
		if _, err := js.AddStream(&nats.StreamConfig{Name: "SLOW" + strconv.Itoa(i), Subjects: []string{"slow." + strconv.Itoa(i)}, Storage: nats.FileStorage}); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	nc.Close()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 60*time.Second); err != nil {
		t.Fatalf("seeding server exited %v", err)
	}
}

// logStamp parses the timestamp of the first log line containing marker.
func logStamp(t *testing.T, path, marker string) (time.Time, bool) {
	t.Helper()
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		// "[pid] 2026/10/26 21:47:03.637881 [INF] ..."
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		ts, err := time.ParseInLocation("2006/01/02 15:04:05.000000", f[1]+" "+f[2], time.Local)
		if err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

// TestSIGTERMDuringSlowStartExits: a SIGTERM that arrives while the server is still inside
// Start, recovering its JetStream store, shuts it down at once, as upstream's handler (installed
// at the end of NewServer) does. A signal loop started only after Run returned would leave the
// signal waiting for the whole recovery, which on a large single-server store is minutes, and the
// server would finish starting first. The store here holds enough streams for recovery to take a
// visible fraction of a second; the assertion is that the server never reports itself ready.
func TestSIGTERMDuringSlowStartExits(t *testing.T) {
	f := newFixture(t)
	slowStartStore(t, f, 4000)
	p := launch(t, f, "slow-start.log")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, ok := logStamp(t, p.log, "Starting JetStream"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never began starting JetStream")
		}
		time.Sleep(2 * time.Millisecond)
	}
	sent := time.Now()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 30*time.Second); err != nil {
		t.Fatalf("exit after SIGTERM during start = %v, want 0", err)
	}
	// ORDER, not presence: "Server is ready" is logged by a defer in Server.Start, so it is
	// printed even when a shutdown cut the start short (measured on CI: trapped, JetStream shut
	// down, then the deferred "ready", then "Server Exiting"). What separates the fix from its
	// absence is whether the signal was acted on before Start returned.
	log := string(mustRead(t, p.log))
	trapped := strings.Index(log, `Trapped "terminated" signal`)
	if trapped < 0 {
		t.Fatal("the server did not trap the SIGTERM")
	}
	if ready := strings.Index(log, "Server is ready"); ready >= 0 && ready < trapped {
		// Tell the fixture being too fast apart from the defect: if the server was ready before
		// the signal was even sent, recovery outran the test and proves nothing either way.
		if at, ok := logStamp(t, p.log, "Server is ready"); ok && at.Before(sent) {
			t.Fatalf("FIXTURE TOO FAST: the server was ready (%v) before the SIGTERM was sent (%v); raise the stream count in slowStartStore", at, sent)
		}
		t.Fatal("the server finished starting before acting on a SIGTERM sent during JetStream recovery: signals are not handled until Run returns")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// waitLog bounds startup/signal synchronization without guessing how long recovery takes.
func (p *proc) waitLog(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(string(mustRead(t, p.log)), marker) {
			return
		}
		if !p.alive() || time.Now().After(deadline) {
			t.Fatalf("server did not log %q while alive", marker)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// blockedStartup holds Start in EnableJetStream's os.ReadFile(metafile). The account
// directory is its configured name (APP), below the normalized jetstream store directory.
// Recovery releases js.mu/a.mu before reading, and parallelTaskQueue workers are not in
// s.grWG, so Shutdown can finish even while this read is blocked. No writer is opened
// until the test explicitly resumes startup or cleanup runs AFTER the exit assertion.
func blockedStartup(t *testing.T) (*proc, string) {
	t.Helper()
	f := newFixture(t)
	conf := filepath.Join(f.dir, "nats.conf")
	if err := os.WriteFile(conf, append(mustRead(t, conf), []byte("\ndebug: true\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(f.dir, "js", "jetstream", "APP", "streams", "BLOCKED", "meta.inf")
	if err := os.MkdirAll(filepath.Dir(meta), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(meta, 0o600); err != nil {
		t.Fatal(err)
	}
	p := launch(t, f, "blocked-start.log")
	// Registered after launch: release a reader before launch's kill-and-wait cleanup.
	t.Cleanup(func() {
		fd, err := syscall.Open(meta, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = syscall.Close(fd)
		} else if !errors.Is(err, syscall.ENXIO) {
			t.Errorf("release recovery FIFO: %v", err)
		}
	})
	p.waitLog(t, `Recovering JetStream state for account "APP"`)
	return p, meta
}

// changeConfig makes the next SIGHUP a FULL reload (a changed digest), the one that reaches
// Server.ReloadOptions and, while startup is incomplete, blocks there. An unchanged file takes the
// hot path (hottls.go), which never calls the server and so could not show the loop parking.
func (p *proc) changeConfig(t *testing.T) {
	t.Helper()
	conf := filepath.Join(p.dir, "nats.conf")
	if err := os.WriteFile(conf, append(mustRead(t, conf), []byte("\ntrace: true\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSIGHUPDuringBlockedStartupDoesNotParkSIGTERM(t *testing.T) {
	p, _ := blockedStartup(t)
	p.changeConfig(t)
	if err := p.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	// Wait only for the loop to have TAKEN the SIGHUP, not for the deferral, so that a loop that
	// handles it inline (and parks in ReloadOptions until startup completes) fails on the exit.
	p.waitLog(t, `Trapped "hangup" signal`)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("SIGTERM after a SIGHUP during startup: %v, want exit 0", err)
	}
	if !strings.Contains(string(mustRead(t, p.log)), "Server is still starting, config reload will run once startup completes") {
		t.Fatal("the reload was not deferred")
	}
}

func TestSIGUSR2DuringBlockedStartupDoesNotDisableSIGTERM(t *testing.T) {
	p, _ := blockedStartup(t)
	if err := p.cmd.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	// Wait only for the loop to have TAKEN the SIGUSR2, so a loop that records a lame duck
	// request the server cannot honour (and then ignores SIGTERM) fails on the exit.
	p.waitLog(t, `Trapped "user defined signal 2" signal`)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("SIGTERM after startup SIGUSR2: %v, want exit 0", err)
	}
	if !strings.Contains(string(mustRead(t, p.log)), "Lame duck mode is not entered: the client listener is not up") {
		t.Fatal("the lame duck request during startup was not reported")
	}
	if strings.Contains(string(mustRead(t, p.log)), "Entering lame duck mode") {
		t.Fatal("entered lame duck mode during startup")
	}
}

func TestDeferredSIGHUPRunsAfterStartup(t *testing.T) {
	p, meta := blockedStartup(t)
	p.changeConfig(t)
	if err := p.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	p.waitLog(t, "Server is still starting, config reload will run once startup completes")
	// A second signal must be handled while the reload waits outside the loop.
	if err := p.cmd.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	p.waitLog(t, "Lame duck mode is not entered: the client listener is not up")
	deadline := time.Now().Add(5 * time.Second)
	for {
		fd, err := syscall.Open(meta, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = syscall.Close(fd)
			break
		}
		if !errors.Is(err, syscall.ENXIO) || time.Now().After(deadline) {
			t.Fatalf("open recovery FIFO writer: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// EOF makes recovery skip this synthetic stream (it has no checksum file).
	// This also proves that recovery actually reached the FIFO before startup resumed.
	p.waitLog(t, "Missing stream checksum file")
	p.waitLog(t, "Reloaded server configuration")
	p.waitLog(t, "Server is ready")
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.waitExit(t, 10*time.Second); err != nil {
		t.Fatalf("SIGTERM after resumed startup: %v, want exit 0", err)
	}
}

func TestCheckReloadFlag(t *testing.T) {
	bin := buildBinary(t)
	f := newFixture(t)
	current := filepath.Join(f.dir, "nats.conf")
	original := string(mustRead(t, current))
	for _, tc := range []struct {
		name, config string
		code         int
		marker       string
	}{
		// checkReload rejects a changed store_dir with ErrConfigReloadNotSupported.
		{"restart", strings.Replace(original, filepath.Join(f.dir, "js"), filepath.Join(f.dir, "other-js"), 1), 3, "jetstream storage dir"},
		// Debug is explicitly handled by diffOptions' debugOption.
		{"reload", original + "\ndebug: true\n", 0, "can be reloaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := filepath.Join(f.dir, tc.name+".conf")
			if tc.config == original {
				t.Fatal("fixture mutation did not change the config")
			}
			if err := os.WriteFile(next, []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			before, err := server.ProcessConfigFile(current)
			if err != nil {
				t.Fatal(err)
			}
			after, err := server.ProcessConfigFile(next)
			if err != nil {
				t.Fatal(err)
			}
			err = server.CheckReload(before, after)
			if tc.code == 3 && !errors.Is(err, server.ErrConfigReloadNotSupported) {
				t.Fatalf("CheckReload = %v, want ErrConfigReloadNotSupported", err)
			} else if tc.code == 0 && err != nil {
				t.Fatalf("CheckReload = %v, want reloadable", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-t", "-c", current, "--test_reload", next)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			err = cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code || !strings.Contains(stderr.String(), tc.marker) {
				t.Fatalf("--test_reload: exit %d, want %d; stderr:\n%s", code, tc.code, stderr.String())
			}
			if tc.code == 0 {
				valid := strings.Index(stderr.String(), "is valid")
				reloadable := strings.Index(stderr.String(), "can be reloaded")
				if valid < 0 || valid > reloadable {
					t.Fatalf("missing or out-of-order validation messages:\n%s", stderr.String())
				}
			}
		})
	}
}
