// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func controlAddressHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// Repeat without saving records: provisioning has not finished in either caller.
func TestNextControlAddrConcurrent(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			controlAddressHome(t)
			start := make(chan struct{})
			type result struct {
				addr string
				err  error
			}
			results := make(chan result, 2)
			for _, name := range []string{"first", "second"} {
				go func(name string) {
					<-start
					addr, _, err := NextControlAddr(name)
					results <- result{addr, err}
				}(name)
			}
			close(start)
			a, b := <-results, <-results
			if a.err != nil || b.err != nil {
				t.Fatalf("reservation errors: %v, %v", a.err, b.err)
			}
			if a.addr == b.addr {
				t.Fatalf("concurrent creates reserved the same address: %s", a.addr)
			}
		})
	}
}

func TestControlClaimStaleReclaim(t *testing.T) {
	controlAddressHome(t)
	addr, releaseOld, err := NextControlAddr("crashed")
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := NextControlAddr("fresh"); err != nil || got == addr {
		t.Fatalf("fresh claim was overlooked: %q, %v", got, err)
	}
	dir, _ := stateDir()
	claims, err := filepath.Glob(filepath.Join(dir, ".control-claims", "8090", "*.claim"))
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %v, error = %v", claims, err)
	}
	old := time.Now().Add(-controlClaimMaxAge - time.Hour)
	if err := os.Chtimes(claims[0], old, old); err != nil {
		t.Fatal(err)
	}
	got, release, err := NextControlAddr("replacement")
	if err != nil || got != addr {
		t.Fatalf("stale claim was not reclaimed: %q, %v; want %q", got, err, addr)
	}
	// A delayed cleanup of the expired generation must not remove its successor.
	if err := releaseOld(); err != nil {
		t.Fatal(err)
	}
	if got, _, err := NextControlAddr("third"); err != nil || got == addr {
		t.Fatalf("expired holder released its successor's claim: %q, %v", got, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if got, _, err := NextControlAddr("after-release"); err != nil || got != addr {
		t.Fatalf("released replacement still reserved the port: %q, %v", got, err)
	}
}

// Exercise callers whose directory listings overlap another caller's release.
func TestNextControlAddrConcurrentRelease(t *testing.T) {
	controlAddressHome(t)
	var mu sync.Mutex
	active := make(map[string]string)
	start := make(chan struct{})
	done := make(chan struct{}, 8)
	for worker := 0; worker < 8; worker++ {
		go func(worker int) {
			defer func() { done <- struct{}{} }()
			<-start
			for attempt := 0; attempt < 32; attempt++ {
				name := fmt.Sprintf("worker-%d-%d", worker, attempt)
				addr, release, err := NextControlAddr(name)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if other, ok := active[addr]; ok {
					t.Errorf("%s and %s both hold %s", name, other, addr)
				}
				active[addr] = name
				mu.Unlock()
				runtime.Gosched()
				mu.Lock()
				if err := release(); err != nil {
					t.Error(err)
				}
				delete(active, addr)
				mu.Unlock()
			}
		}(worker)
	}
	close(start)
	for worker := 0; worker < 8; worker++ {
		<-done
	}
}

func TestControlClaimReleaseAndRecordHandoff(t *testing.T) {
	controlAddressHome(t)
	addr, release, err := NextControlAddr("mine")
	if err != nil {
		t.Fatal(err)
	}
	// Re-creates do not count their own claims, but each owns its release.
	again, releaseAgain, err := NextControlAddr("mine")
	if err != nil || again != addr {
		t.Fatalf("own claim counted against re-create: %q, %v", again, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if got, otherRelease, err := NextControlAddr("other"); err != nil {
		t.Fatal(err)
	} else {
		if got == addr {
			t.Fatal("release removed another create's claim")
		}
		if err := otherRelease(); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(&Record{Name: "mine", ControlAddr: addr}); err != nil {
		t.Fatal(err)
	}
	if err := releaseAgain(); err != nil {
		t.Fatal(err)
	}
	if got, _, err := NextControlAddr("after-save"); err != nil || got == addr {
		t.Fatalf("saved record stopped reserving its port: %q, %v", got, err)
	}
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
			got, _, err := NextControlAddr("new")
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
	if _, _, err := NextControlAddr("new"); err == nil || !strings.Contains(err.Error(), "--control-addr") {
		t.Fatalf("exhaustion must name --control-addr: %v", err)
	}
	if got, _, err := NextControlAddr("sim-8090"); err != nil || got != "http://localhost:8090" {
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
	if _, _, err := NextControlAddr("new"); err == nil {
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
	if got, _, err := NextControlAddr("mine"); err != nil || got != "http://localhost:8091" {
		t.Fatalf("re-create moved off its own free port: %q, %v", got, err)
	}
	if err := Save(&Record{Name: "other", ControlAddr: "http://127.0.0.1:8091"}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := NextControlAddr("mine"); err != nil || got != "http://localhost:8090" {
		t.Fatalf("re-create kept a port another sim names: %q, %v", got, err)
	}
}
