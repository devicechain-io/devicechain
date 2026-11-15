// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMintFixture(t *testing.T, root, name, source string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Each planted defect must be named, not merely cause some unrelated failure.
func TestServiceMintFixtureNegativeControls(t *testing.T) {
	root := t.TempDir()
	writeMintFixture(t, root, "backend/core/auth/authority.go", `package auth
type Authority string
const (TenantRead Authority = "tenant:read"; DeviceRead Authority = "device:read")`)
	writeMintFixture(t, root, "backend/services/fixture/main.go", `package fixture
import (
 a "github.com/devicechain-io/dc-microservice/auth"
 s "github.com/devicechain-io/dc-microservice/svcclient"
 g "github.com/devicechain-io/dc-microservice/governance"
 service "github.com/devicechain-io/dc-microservice/service"
)
var _ = service.NatsSpec{}
func overLiteral() { s.New(nil, "", "fixture", []string{string(a.DeviceRead)}) }
func dynamicSubject(subject string) { s.New(nil, "", subject, []string{a.TenantRead}) }
func overForwarder() { g.NewTenantLifecycleGate(nil, "", "forwarded", nil) }
func dynamicAuthorities(grants []string) { s.New(nil, "", "fixture", grants) }
func valid() { s.New(nil, "", "fixture", []string{string(a.TenantRead)}) }
`)
	writeMintFixture(t, root, "backend/services/missing/main.go", "package missing")
	writeMintFixture(t, root, "backend/services/mcp/main.go", "package mcp")
	// Invalid syntax demonstrates these files are never parsed.
	writeMintFixture(t, root, "backend/services/fixture/ignored_test.go", "not Go")
	writeMintFixture(t, root, "backend/services/fixture/testdata/ignored.go", "not Go")
	sites, err := serviceMintSitesUnder(root, serviceMintForwarders)
	if err != nil {
		t.Fatal(err)
	}
	areaCount := 0
	for _, site := range sites {
		if site.Kind == "area" && site.Caller == "fixture" {
			areaCount++
		}
	}
	if areaCount != 1 {
		t.Fatalf("area attribution: got %d sites, want one", areaCount)
	}
	problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}, "forwarded": {"device:read"}}), "\n")
	for _, name := range []string{"overLiteral", "dynamicSubject", "overForwarder", "dynamicAuthorities", "caller missing absent"} {
		if !strings.Contains(problems, name) {
			t.Errorf("planted %s was not reported:\n%s", name, problems)
		}
	}
	for _, name := range []string{"fixture/main.go:valid:", "caller mcp absent"} {
		if strings.Contains(problems, name) {
			t.Errorf("unexpected finding %s:\n%s", name, problems)
		}
	}
	// Repair the planted sites; the same scanner and judgement must now pass.
	writeMintFixture(t, root, "backend/services/fixture/main.go", `package fixture
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
func valid() { s.New(nil, "", "fixture", []string{string(a.TenantRead)}) }`)
	writeMintFixture(t, root, "backend/services/missing/main.go", `package missing
import g "github.com/devicechain-io/dc-microservice/governance"
func valid() { g.NewTenantLifecycleGates(nil, "", "missing", nil) }`)
	writeMintFixture(t, root, "backend/core/governance/lifecycle_resolver.go", `package governance
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
func NewTenantLifecycleGate(cfg any, secret, subject string, gates any) { NewTenantLifecycleGates(cfg, secret, subject, gates) }
func NewTenantLifecycleGates(cfg any, secret, subject string, gates any) { s.New(cfg, secret, subject, []string{string(a.TenantRead)}) }`)
	sites, err = serviceMintSitesUnder(root, serviceMintForwarders)
	if err != nil {
		t.Fatal(err)
	}
	if problems := checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}, "missing": {"tenant:read"}}); len(problems) != 0 {
		t.Fatal(problems)
	}
	// A ledger entry excuses the dynamic subject, not changes to its grant.
	writeMintFixture(t, root, "backend/core/governance/lifecycle_resolver.go", `package governance
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
func NewTenantLifecycleGates(cfg any, secret, subject string, gates any) { s.New(cfg, secret, subject, []string{string(a.DeviceRead)}) }`)
	sites, err = serviceMintSitesUnder(root, serviceMintForwarders)
	if err != nil {
		t.Fatal(err)
	}
	if problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}, "missing": {"tenant:read"}}), "\n"); !strings.Contains(problems, "NewTenantLifecycleGates: non-literal subject") {
		t.Fatalf("damaged forwarder grant was not reported: %s", problems)
	}
}

func TestServiceMintDamagedTableNegativeControls(t *testing.T) {
	sites, err := serviceMintSitesUnder(workspaceRoot(t), serviceMintForwarders)
	if err != nil {
		t.Fatal(err)
	}
	for _, damage := range []string{"removed", "added"} {
		t.Run(damage, func(t *testing.T) {
			ceiling := serviceMintCeiling()
			if damage == "removed" {
				ceiling["ai-inference"] = nil
			} else {
				ceiling["ai-inference"] = append(ceiling["ai-inference"], "device:write")
			}
			problems := strings.Join(checkMintSites(sites, ceiling), "\n")
			if !strings.Contains(problems, "caller ai-inference") {
				t.Fatalf("%s grant was not detected: %s", damage, problems)
			}
		})
	}
}
