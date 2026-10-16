// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"reflect"
	"testing"
	"time"
)

func TestSimRunArgs(t *testing.T) {
	got, err := simRunArgs(simRunOptions{
		handshake:    "/home/me/.devicechain/sims/sitepulse.json",
		controlAddr:  "http://localhost:8091",
		playDevices:  true,
		publishLog:   "published.ndjson",
		emitInterval: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--handshake", "/home/me/.devicechain/sims/sitepulse.json",
		"--port", "8091",
		"--play-devices",
		"--publish-log", "published.ndjson",
		"--emit-interval", "3s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("simRunArgs = %q, want %q", got, want)
	}

	// The defaults pass nothing the simulator would not choose itself.
	got, err = simRunArgs(simRunOptions{handshake: "h.json", controlAddr: "http://localhost:8090"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--handshake", "h.json", "--port", "8090"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("simRunArgs = %q, want %q", got, want)
	}
}

func TestSimRunRefusesWhatCannotWork(t *testing.T) {
	if _, err := simRunArgs(simRunOptions{handshake: "h.json", controlAddr: "http://localhost:8090", publishLog: "x"}); err == nil {
		t.Error("a publish log without --play-devices was accepted; it would stay empty")
	}
	if _, err := simRunArgs(simRunOptions{handshake: "h.json", controlAddr: "http://localhost"}); err == nil {
		t.Error("a control address with no port was accepted; 'sim status' could not reach the process")
	}
}
