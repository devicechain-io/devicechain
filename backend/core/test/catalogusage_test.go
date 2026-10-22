// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

// writeTree lays out a fake services/ tree and the two declaration files the scan reads.
func writeTree(t *testing.T, files map[string]string) (services, streamsFile, kvFile string) {
	t.Helper()
	dir := t.TempDir()
	files["decl/streams.go"] = "package streams\nconst (\n\tInboundEvents = \"inbound-events\"\n\tDeadLetters = \"dead-letters\"\n)\n"
	files["decl/kv.go"] = "package kv\nconst BucketLocks = \"dc_locks\"\n"
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "services"), filepath.Join(dir, "decl", "streams.go"), filepath.Join(dir, "decl", "kv.go")
}

// The scan reads what it should and says so when it cannot: it resolves a streams.X and a
// kv.X argument, folds a package-local constant, passes over a stdlib constructor that shares
// a method's name, and REPORTS a stream chosen at run time instead of dropping it.
func TestScanReadsCallsAndReportsWhatItCannotResolve(t *testing.T) {
	services, sf, kf := writeTree(t, map[string]string{
		"services/event-sources/main.go": `package main
import ("bufio"; "os")
const local = streams.DeadLetters
func f(nmgr *M, suffix string) {
	nmgr.NewWriter(streams.InboundEvents)
	nmgr.NewReader(local)
	nmgr.KeyValueStore(kv.BucketLocks, "x", 0)
	nmgr.NewDistributedLease(0)
	bufio.NewWriter(os.Stdout)
	nmgr.NewWriter(suffix)
}
`,
	})
	uses, unresolved, _, err := scanCatalogUses(services, sf, kf, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[catalogUse]bool{}
	for _, u := range uses {
		got[catalogUse{Area: u.Area, Role: u.Role, Name: u.Name}] = true
	}
	for _, want := range []catalogUse{
		{Area: "event-sources", Role: useWrite, Name: "inbound-events"},
		{Area: "event-sources", Role: useRead, Name: "dead-letters"},
		{Area: "event-sources", Role: useBucket, Name: "dc_locks"},
		{Area: "event-sources", Role: useBucket, Name: "dc_leases"},
	} {
		if !got[want] {
			t.Errorf("scan missed %+v; found %v", want, uses)
		}
	}
	if len(uses) != 4 {
		t.Errorf("scan found %d uses, want 4 (bufio.NewWriter is not the manager's): %v", len(uses), uses)
	}
	if len(unresolved) != 1 {
		t.Errorf("a stream chosen at run time must be reported, got %v", unresolved)
	}
}

// The negative control for the test above it: the declarations judge a use that is not
// declared. event-sources is a real area, device-commands a real stream it does not write.
func TestAnUndeclaredUseIsJudgedUndeclared(t *testing.T) {
	if declaredFor(catalogUse{Area: "event-sources", Role: useWrite, Name: streams.DeviceCommands}) {
		t.Error("event-sources was judged a declared writer of device-commands")
	}
	if !declaredFor(catalogUse{Area: "event-sources", Role: useWrite, Name: streams.InboundEvents}) {
		t.Error("event-sources was not judged a declared writer of inbound-events")
	}
	if declaredFor(catalogUse{Area: "event-sources", Role: "no-such-role", Name: streams.InboundEvents}) {
		t.Error("an unknown role was judged declared")
	}
}

// A scan of a tree with no Go files must refuse rather than report a clean tree.
func TestScanOfNothingRefuses(t *testing.T) {
	services, sf, kf := writeTree(t, map[string]string{})
	if err := os.MkdirAll(services, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := scanCatalogUses(services, sf, kf, nil, nil); err == nil {
		t.Fatal("a scan that read no files returned no error")
	}
}

// The scan has to be looking at the real tree: aim it at a use that is known to exist.
func TestScanFindsKnownUsesInTheRealTree(t *testing.T) {
	root := workspaceRoot(t)
	uses, _, _, err := scanCatalogUses(filepath.Join(root, "backend", "services"),
		filepath.Join(root, "backend", "core", "streams", "streams.go"),
		filepath.Join(root, "backend", "core", "kv", "buckets.go"), dynamicCatalogUses, catalogAliases)
	if err != nil {
		t.Fatal(err)
	}
	got := usedBy(uses)
	for _, want := range []catalogUse{
		{Area: "event-sources", Role: useWrite, Name: streams.InboundEvents},
		{Area: "device-management", Role: useRead, Name: streams.InboundEvents},
		{Area: "event-processing", Role: useReplay, Name: streams.ResolvedEvents},
		{Area: "event-management", Role: useLive, Name: streams.ResolvedEvents},
		{Area: "device-management", Role: useBucket, Name: "device-by-token"},
		{Area: "user-management", Role: useBucket, Name: "dc_refresh_tokens"},
	} {
		if !got[want] {
			t.Errorf("the scan of the real tree did not find %+v; it is not reading what it claims to (%d uses found)", want, len(uses))
		}
	}
}
