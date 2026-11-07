// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-broker/nats-server/internal/smoke"
)

// rotateAndReload connects a fleet, rotates the broker's certificate on disk, applies reload, and
// returns how many sessions were lost. It also asserts the rotation took effect: the NEXT handshake
// presents the new certificate.
func rotateAndReload(t *testing.T, fleet int, reload func(*running) error) int {
	t.Helper()
	r := startBroker(t, true)
	f, err := smoke.HoldFleet(r.dir, r.nats, r.mqtt, fleet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)

	before, err := smoke.ServedSerial(r.dir, r.mqtt)
	if err != nil {
		t.Fatal(err)
	}
	want, err := smoke.RotateServerCert(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if want == before {
		t.Fatal("rotation did not change the serial")
	}
	if err := reload(r); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := smoke.ServedSerial(r.dir, r.mqtt)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("a new handshake saw serial %s, want the rotated %s", got, want)
	}
	// A close is asynchronous to the reload: give it time to arrive.
	time.Sleep(2 * time.Second)
	n := f.Lost()
	if n == 0 {
		if err := f.Publish(); err != nil {
			t.Fatalf("a surviving session does not work: %v", err)
		}
	} else {
		t.Logf("first loss: %s", f.FirstLoss())
	}
	return n
}

// TestTLSRotationReloadKeepsMQTTSessions is the D-AB2 reproduction: a certificate rotation as
// cert-manager performs it (new leaf, same CA, files rewritten in place), followed by the reload
// the SIGHUP path performs, must leave every established MQTT session connected, and the NEXT
// handshake must present the new certificate.
func TestTLSRotationReloadKeepsMQTTSessions(t *testing.T) {
	const fleet = 20
	if n := rotateAndReload(t, fleet, func(r *running) error { return r.b.reload() }); n != 0 {
		t.Fatalf("the reload dropped %d of %d established MQTT sessions", n, fleet)
	}
}

// TestFullReloadKeepsCalloutSessions is the regression test for the callout-reload defect. Before
// nats-server carried the upstream fix (v2.15.3), handing the server new options, even for a pure
// certificate rotation, closed every callout-admitted session. It must now keep all of them.
func TestFullReloadKeepsCalloutSessions(t *testing.T) {
	const fleet = 20
	if n := rotateAndReload(t, fleet, func(r *running) error { return r.b.reloadFull() }); n != 0 {
		t.Fatalf("a full reload dropped %d of %d callout-admitted sessions", n, fleet)
	}
}

// TestConfigChangeKeepsCalloutSessions: a change to the configuration that is not key material
// (here the auth timeout) takes the full reload, applies, and leaves the fleet connected.
func TestConfigChangeKeepsCalloutSessions(t *testing.T) {
	const fleet = 20
	r := startBroker(t, true)
	f, err := smoke.HoldFleet(r.dir, r.nats, r.mqtt, fleet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	conf := filepath.Join(r.dir, "nats.conf")
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	const from, to = "timeout: 5\n", "timeout: 7\n"
	if !strings.Contains(string(b), from) {
		t.Fatal("fixture changed: no authorization timeout to edit")
	}
	if err := os.WriteFile(conf, []byte(strings.Replace(string(b), from, to, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	if r.b.hotSwaps.Load() != 0 {
		t.Fatal("a config change was served by the hot path; the full reload was not exercised")
	}
	if vz, err := r.s.Varz(nil); err != nil || vz.AuthTimeout != 7 {
		t.Fatalf("reload did not apply the change: %v %v", vz, err)
	}
	time.Sleep(2 * time.Second)
	if n := f.Lost(); n != 0 {
		t.Fatalf("a configuration reload dropped %d of %d sessions: %s", n, fleet, f.FirstLoss())
	}
	if err := f.Publish(); err != nil {
		t.Fatalf("a surviving session does not work: %v", err)
	}
}

// TestConfigChangeFallsBackToFullReload: a reload that is not key material only goes to the server
// (here a changed auth timeout), so the hot path never hides a real config change.
func TestConfigChangeFallsBackToFullReload(t *testing.T) {
	r := startBroker(t, true)
	conf := filepath.Join(r.dir, "nats.conf")
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	const from, to = "timeout: 5\n", "timeout: 7\n"
	if !strings.Contains(string(b), from) {
		t.Fatal("fixture changed: no authorization timeout to edit")
	}
	if err := os.WriteFile(conf, []byte(strings.Replace(string(b), from, to, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.b.reload(); err != nil {
		t.Fatal(err)
	}
	vz, err := r.s.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}
	if vz.AuthTimeout != 7 {
		t.Fatalf("auth timeout is %v after a config change + reload, want 7: the change was swallowed", vz.AuthTimeout)
	}
}
