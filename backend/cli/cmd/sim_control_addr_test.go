// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/sim"
)

func TestSimCreateSelectsControlAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		occupied []string
		explicit string
		want     string
	}{
		{"next port", []string{"http://localhost:8090"}, "", "http://localhost:8091"},
		{"first gap", []string{"http://localhost:8090", "http://localhost:8092"}, "", "http://localhost:8091"},
		{"IPv4 loopback", []string{"http://127.0.0.1:8090"}, "", "http://localhost:8091"},
		{"IPv6 loopback", []string{"http://[::1]:8090"}, "", "http://localhost:8091"},
		{"explicit default", []string{"http://localhost:8090"}, "http://localhost:8090", "http://localhost:8090"},
		{"explicit verbatim", []string{"http://localhost:8090"}, "http://127.0.0.1:9000/", "http://127.0.0.1:9000/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedHome(t)
			stubRecordedHost(t, "", nil)
			for i, addr := range tc.occupied {
				if err := sim.Save(&sim.Record{Name: strings.Repeat("a", i+1), ControlAddr: addr}); err != nil {
					t.Fatal(err)
				}
			}
			srv, _ := aHostRecordingGateway(t)
			argv := []string{"--server", strings.TrimPrefix(srv.URL, "http://")}
			if tc.explicit != "" {
				argv = append(argv, "--control-addr", tc.explicit)
			}
			if err := runSimCreate(freshSimCommand(t, simCreateCmd, argv...), []string{"probe"}); err != nil {
				t.Fatal(err)
			}
			rec, err := sim.Load("probe")
			if err != nil {
				t.Fatal(err)
			}
			if rec.ControlAddr != tc.want {
				t.Fatalf("saved control address = %q, want %q", rec.ControlAddr, tc.want)
			}
		})
	}
}

func TestSimCreateReleasesControlClaimOnFailure(t *testing.T) {
	isolatedHome(t)
	// Selection happens before manifest validation, so this fails with a claim held.
	err := runSimCreate(freshSimCommand(t, simCreateCmd, "--manifest", "unknown-manifest"), []string{"failed"})
	if err == nil || !strings.Contains(err.Error(), "unknown manifest") {
		t.Fatalf("expected manifest validation failure, got %v", err)
	}
	if got, _, err := sim.NextControlAddr("next"); err != nil || got != "http://localhost:8090" {
		t.Fatalf("failed create leaked its claim: %q, %v", got, err)
	}
}

func TestSimCreateControlAddressExhaustionAndRecreate(t *testing.T) {
	isolatedHome(t)
	stubRecordedHost(t, "", nil)
	for p := 8090; p <= 8189; p++ {
		if err := sim.Save(&sim.Record{
			Name: fmt.Sprintf("sim-%d", p), ControlAddr: fmt.Sprintf("http://localhost:%d", p), SimPassword: "keep-me",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Selection must fail before any platform request when the range is exhausted.
	if err := runSimCreate(freshSimCommand(t, simCreateCmd), []string{"new"}); err == nil || !strings.Contains(err.Error(), "--control-addr") {
		t.Fatalf("exhaustion must name --control-addr: %v", err)
	}
	srv, _ := aHostRecordingGateway(t)
	argv := []string{"--server", strings.TrimPrefix(srv.URL, "http://")}
	if err := runSimCreate(freshSimCommand(t, simCreateCmd, argv...), []string{"sim-8090"}); err != nil {
		t.Fatal(err)
	}
	rec, err := sim.Load("sim-8090")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ControlAddr != "http://localhost:8090" || rec.SimPassword != "keep-me" {
		t.Fatal("re-create must exclude its own port and retain its password")
	}
	argv = append(argv, "--control-addr", "http://localhost:8090")
	if err := runSimCreate(freshSimCommand(t, simCreateCmd, argv...), []string{"new"}); err != nil {
		t.Fatalf("an explicit address must bypass automatic selection: %v", err)
	}
}
