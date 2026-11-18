// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic forwarders exercise literal subjects without restoring removed production APIs.
func fixtureMintForwarders() map[string]mintForwarder {
	forwarders := map[string]mintForwarder{}
	for name, forwarder := range serviceMintForwarders {
		forwarders[name] = forwarder
	}
	forwarders["governance.FixtureGate"] = mintForwarder{2, []string{"tenant:read"}}
	forwarders["governance.FixtureGates"] = mintForwarder{2, []string{"tenant:read"}}
	return forwarders
}

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
	writeDeadletterMintFixture(t, root)
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
func overForwarder() { g.FixtureGate(nil, "", "forwarded", nil) }
func dynamicAuthorities(grants []string) { s.New(nil, "", "fixture", grants) }
func valid() { s.New(nil, "", "fixture", []string{string(a.TenantRead)}) }
`)
	writeMintFixture(t, root, "backend/services/missing/main.go", "package missing")
	writeMintFixture(t, root, "backend/services/mcp/main.go", "package mcp")
	// Invalid syntax demonstrates these files are never parsed.
	writeMintFixture(t, root, "backend/services/fixture/ignored_test.go", "not Go")
	writeMintFixture(t, root, "backend/services/fixture/testdata/ignored.go", "not Go")
	sites, err := serviceMintSitesUnder(root, fixtureMintForwarders())
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
func valid() { g.FixtureGates(nil, "", "missing", nil) }`)
	writeMintFixture(t, root, "backend/core/governance/lifecycle_resolver.go", `package governance
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
func FixtureGate(cfg any, secret, subject string, gates any) { FixtureGates(cfg, secret, subject, gates) }
func FixtureGates(cfg any, secret, subject string, gates any) { s.New(cfg, secret, subject, []string{string(a.TenantRead)}) }`)
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
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
func FixtureGates(cfg any, secret, subject string, gates any) { s.New(cfg, secret, subject, []string{string(a.DeviceRead)}) }`)
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	if problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}, "missing": {"tenant:read"}}), "\n"); !strings.Contains(problems, "FixtureGates: non-literal subject") {
		t.Fatalf("damaged forwarder grant was not reported: %s", problems)
	}
}

func writeDeadletterMintFixture(t *testing.T, root string) {
	t.Helper()
	writeMintFixture(t, root, "backend/core/deadletter/recorder.go", `package deadletter
import g "github.com/devicechain-io/dc-microservice/governance"
var tenantLifecycleGate = g.NewTenantLifecycleGateFor`)
	writeMintFixture(t, root, "backend/core/deadletter/deadletter.go", `package deadletter
import s "github.com/devicechain-io/dc-microservice/svcclient"
func gate() { peers := s.NewPeers(infra, p.ms.FunctionalArea); tenantLifecycleGate(peers, nil) }`)
}

// These trees deliberately contain function-value mints and forbidden imports.
// Repairing each tree also proves its finding is not an unrelated ceiling error.
func TestServiceMintReferenceNegativeControls(t *testing.T) {
	for _, fixture := range []struct {
		name, source, finding string
	}{
		{"client alias", `import s "github.com/devicechain-io/dc-microservice/svcclient"
func escaped() { mint := s.New; mint(nil, "", "fixture", nil) }`, "main.go:escaped: constructor reference"},
		{"client argument", `import s "github.com/devicechain-io/dc-microservice/svcclient"
func escaped() { consume(s.New) }`, "main.go:escaped: constructor reference"},
		{"gate alias", `import g "github.com/devicechain-io/dc-microservice/governance"
func escaped() { mint := g.FixtureGate; mint(nil, "", "fixture", nil) }`, "main.go:escaped: constructor reference"},
		{"gates argument", `import g "github.com/devicechain-io/dc-microservice/governance"
func escaped() { consume(g.FixtureGates) }`, "main.go:escaped: constructor reference"},
		{"client dot import", `import . "github.com/devicechain-io/dc-microservice/svcclient"
func escaped() { New(nil, "", "fixture", nil) }`, "dot-import of svcclient"},
		{"governance dot import", `import . "github.com/devicechain-io/dc-microservice/governance"
func escaped() { FixtureGate(nil, "", "fixture", nil) }`, "dot-import of governance"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := newAreaMintFixture(t)
			writeMintFixture(t, root, "backend/services/fixture/main.go", "package fixture\n"+fixture.source)
			sites, err := serviceMintSitesUnder(root, fixtureMintForwarders())
			if err != nil {
				t.Fatal(err)
			}
			problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
			if !strings.Contains(problems, fixture.finding) {
				t.Fatalf("planted defect was not reported (%s):\n%s", fixture.finding, problems)
			}
			writeMintFixture(t, root, "backend/services/fixture/main.go", "package fixture")
			assertAreaMintFixturePasses(t, root)
		})
	}
}

func newAreaMintFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMintFixture(t, root, "backend/core/auth/authority.go", `package auth
type Authority string
const TenantRead Authority = "tenant:read"`)
	writeDeadletterMintFixture(t, root)
	writeMintFixture(t, root, "backend/services/fixture/area.go", `package fixture
import service "github.com/devicechain-io/dc-microservice/service"
var _ = service.NatsSpec{}`)
	return root
}

func assertAreaMintFixturePasses(t *testing.T, root string) {
	t.Helper()
	sites, err := serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	if problems := checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestServiceMintDeadletterNegativeControls(t *testing.T) {
	for _, fixture := range []struct {
		name, file, source, finding string
	}{
		{"literal subject", "other.go", `package deadletter
func badGate() { tenantLifecycleGate(nil, "", "unknown", nil) }`, "other.go:badGate: tenantLifecycleGate subject must end in .FunctionalArea"},
		{"wrong constructor", "recorder.go", `package deadletter
import g "github.com/devicechain-io/dc-microservice/governance"
var tenantLifecycleGate = g.FixtureGates`, "tenantLifecycleGate must be initialized to governance.NewTenantLifecycleGateFor"},
		{"missing alias", "recorder.go", "package deadletter", "requires exactly one constructor alias"},
		{"missing call", "deadletter.go", "package deadletter", "at least one FunctionalArea call"},
		{"missing subject", "other.go", `package deadletter
func badGate() { tenantLifecycleGate(nil, "") }`, "other.go:badGate: tenantLifecycleGate missing FunctionalArea subject"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := newAreaMintFixture(t)
			assertAreaMintFixturePasses(t, root)
			path := "backend/core/deadletter/" + fixture.file
			writeMintFixture(t, root, path, fixture.source)
			sites, err := serviceMintSitesUnder(root, fixtureMintForwarders())
			if err != nil {
				t.Fatal(err)
			}
			problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
			if !strings.Contains(problems, fixture.finding) {
				t.Fatalf("planted defect was not reported (%s):\n%s", fixture.finding, problems)
			}
			for _, site := range sites {
				if site.Kind == "area" {
					t.Fatalf("invalid dead-letter mint still attributed to area: %+v", site)
				}
			}
			writeMintFixture(t, root, path, "package deadletter")
			writeDeadletterMintFixture(t, root)
			assertAreaMintFixturePasses(t, root)
		})
	}
}

func TestServiceMintDamagedTableNegativeControls(t *testing.T) {
	sites, err := serviceMintSitesUnder(workspaceRoot(t), fixtureMintForwarders())
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

// Planted peer-wiring defects must be attributed to the service directory and named.
func TestServiceMintPeersNegativeControls(t *testing.T) {
	root := newAreaMintFixture(t)
	writeMintFixture(t, root, "backend/core/auth/authority.go", `package auth
type Authority string
const (TenantRead Authority = "tenant:read"; DeviceRead Authority = "device:read")`)
	source := `package fixture
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
import g "github.com/devicechain-io/dc-microservice/governance"
func peerCalls() {
 peers := s.NewPeers(infra, ms.FunctionalArea)
 peers.Client(a.DeviceRead)
 peers.Seam(target, a.DeviceRead)
 peers.UserManagement(a.DeviceRead)
 tuned := peers.With(option)
 tuned.Client(a.DeviceRead)
 copied := peers
 copied.Client(a.DeviceRead)
 peers.With(option).UserManagement(a.DeviceRead)
 g.NewTenantLifecycleGateFor(peers, nil)
 g.NewTenantLifecycleGatesFor(peers, nil)
}
`
	writeMintFixture(t, root, "backend/services/fixture/peers.go", source)
	sites, err := serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, site := range sites {
		if strings.HasSuffix(site.File, "/peers.go") {
			count++
			if site.Caller != "fixture" || site.Unresolved != "" {
				t.Fatalf("bad attribution: %+v", site)
			}
		}
	}
	if count != 8 {
		t.Fatalf("got %d peer sites, want 8", count)
	}
	problems := strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if strings.Count(problems, "requests device:read outside ceiling") != 6 {
		t.Fatalf("excess peer grants escaped: %s", problems)
	}
	source = strings.ReplaceAll(source, "a.DeviceRead", "a.TenantRead")
	writeMintFixture(t, root, "backend/services/fixture/peers.go", source)
	assertAreaMintFixturePasses(t, root)
	writeMintFixture(t, root, "backend/services/fixture/peers.go", strings.ReplaceAll(source, "ms.FunctionalArea", `"fixture"`))
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if !strings.Contains(problems, "NewPeers subject must end in .FunctionalArea") {
		t.Fatalf("literal area escaped: %s", problems)
	}
	writeMintFixture(t, root, "backend/services/fixture/peers.go", strings.Replace(source, "peers.Client(a.TenantRead)", "alias := peers.Client; _ = alias", 1))
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if !strings.Contains(problems, "constructor reference is not a direct call") {
		t.Fatalf("method alias escaped: %s", problems)
	}
	writeMintFixture(t, root, "backend/services/fixture/peers.go", strings.Replace(source, "peers := s.NewPeers", "factory := s.NewPeers; peers := factory", 1))
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if !strings.Contains(problems, "constructor reference is not a direct call: svcclient.NewPeers") {
		t.Fatalf("factory alias escaped: %s", problems)
	}
	writeMintFixture(t, root, "backend/services/fixture/peers.go", source+`
func nested() { consume(s.NewPeers(infra, "fixture")) }
`)
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if !strings.Contains(problems, "peers.go:nested: NewPeers subject must end in .FunctionalArea") {
		t.Fatalf("nested constructor subject escaped: %s", problems)
	}
	writeMintFixture(t, root, "backend/services/fixture/peers.go", source)
	writeMintFixture(t, root, "backend/core/governance/lifecycle_resolver.go", `package governance
import s "github.com/devicechain-io/dc-microservice/svcclient"
import a "github.com/devicechain-io/dc-microservice/auth"
func NewTenantLifecycleGatesFor(p *s.Peers, gates any) { p.UserManagement(a.DeviceRead) }`)
	sites, err = serviceMintSitesUnder(root, fixtureMintForwarders())
	if err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(checkMintSites(sites, map[string][]string{"fixture": {"tenant:read"}}), "\n")
	if !strings.Contains(problems, "NewTenantLifecycleGatesFor: non-literal subject") {
		t.Fatalf("changed forwarder grant escaped: %s", problems)
	}
}
