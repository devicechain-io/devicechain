// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func controlAddressHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestNextControlAddr(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		want      string
	}{
		{"empty", nil, "http://localhost:8090"},
		{"next", []string{"http://localhost:8090"}, "http://localhost:8091"},
		{"gap", []string{"http://localhost:8090", "http://localhost:8092"}, "http://localhost:8091"},
		{"aliases", []string{"http://127.0.0.1:8090", "http://[::1]:8091"}, "http://localhost:8092"},
		{"any host", []string{"http://example.com:8090"}, "http://localhost:8091"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controlAddressHome(t)
			for i, addr := range tc.addresses {
				if err := Save(&Record{Name: fmt.Sprintf("sim-%d", i), ControlAddr: addr}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := NextControlAddr("new")
			if err != nil || got != tc.want {
				t.Fatalf("address = %q, err = %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestNextControlAddrExhaustionAndRecreate(t *testing.T) {
	controlAddressHome(t)
	for p := 8090; p <= 8189; p++ {
		if err := Save(&Record{Name: fmt.Sprintf("sim-%d", p), ControlAddr: fmt.Sprintf("http://localhost:%d", p)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NextControlAddr("new"); err == nil || !strings.Contains(err.Error(), "--control-addr") {
		t.Fatalf("exhaustion must name --control-addr: %v", err)
	}
	if got, err := NextControlAddr("sim-8090"); err != nil || got != "http://localhost:8090" {
		t.Fatalf("own old record must be excluded: %q, %v", got, err)
	}
}

func TestListFailsClosed(t *testing.T) {
	controlAddressHome(t)
	dir, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file in place of the directory fails even when tests run as root.
	if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("unreadable records directory was ignored")
	}
	if _, err := NextControlAddr("new"); err == nil {
		t.Fatal("unreadable records directory selected a port")
	}
}

func TestListRejectsBrokenRecords(t *testing.T) {
	controlAddressHome(t)
	if err := Save(&Record{Name: "broken"}); err != nil {
		t.Fatal(err)
	}
	path, _ := RecordPath("broken")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("broken record was ignored")
	}
}

// Re-create keeps the sim's recorded address while no other sim names its port, so a
// running sim stays reachable; it moves only when another record shares the port.
func TestNextControlAddrRecreateKeepsOwnPort(t *testing.T) {
	controlAddressHome(t)
	if err := Save(&Record{Name: "mine", ControlAddr: "http://localhost:8091"}); err != nil {
		t.Fatal(err)
	}
	if got, err := NextControlAddr("mine"); err != nil || got != "http://localhost:8091" {
		t.Fatalf("re-create moved off its own free port: %q, %v", got, err)
	}
	if err := Save(&Record{Name: "other", ControlAddr: "http://127.0.0.1:8091"}); err != nil {
		t.Fatal(err)
	}
	if got, err := NextControlAddr("mine"); err != nil || got != "http://localhost:8090" {
		t.Fatalf("re-create kept a port another sim names: %q, %v", got, err)
	}
}
