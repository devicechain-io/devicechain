// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// servedCatalog asks the server THIS SERVICE SERVES what it exposes — over the HTTP
// surface New builds, through the bearer middleware, with a token this server accepts —
// and pairs those names with the declarations register() made while building it.
//
// 🔴 IT LISTS THE SERVED SERVER, NOT ONE THE TEST BUILT, AND THAT DISTINCTION IS THE WHOLE
// VALUE OF THE RATCHET. These helpers used to construct their own mcp.Server and call
// registerTools on it, so "register is the only registration path" was only ever asserted
// about registerTools' source: an mcp.AddTool call added to New — one line after the
// registration, compiling, serving — was invisible to every test in this package. The
// names below come from New's own handler, so a tool added anywhere New can reach it is
// reported by the completeness check like any other undeclared tool.
//
// The catalog comes from newServer, which is the same construction New performs: it can
// only ever hold what register declared, so an extra SERVED tool has no entry in it.
func servedCatalog(t *testing.T) ([]string, *Catalog, []*mcp.Tool) {
	t.Helper()
	_, c := newServer()
	names, tools := listServedTools(t)
	return names, c, tools
}

// listServedTools drives a real MCP session against the handler New returns and returns
// the catalog that session is offered.
func listServedTools(t *testing.T) ([]string, []*mcp.Tool) {
	t.Helper()
	ctx := context.Background()
	iss, validator := mustIssuerValidator(t)
	mcpHandler, _ := New(testResource, "https://as.example.com", validator)
	ts := httptest.NewServer(mcpHandler)
	t.Cleanup(ts.Close)

	// The middleware requires a token bound to THIS resource and carrying the read-only
	// scope; anything else is refused before a session is established, so listing the
	// tools at all exercises the wiring New does around the server as well as the server.
	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-list")
	if err != nil {
		t.Fatalf("issuing a read-only token: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: bearerTransport{token: tok.Token}},
			// Nothing here is worth reconnecting for: a failure is the answer.
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("client connect through the served handler: %v", err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools over the served handler: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names, res.Tools
}

// bearerTransport presents the caller's token on every request the MCP client makes.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// listTools connects a client to a server the CALLER built and returns the catalog it is
// offered. It exists for the negative control below, which needs a throwaway server it can
// deliberately corrupt; every assertion about the real catalog goes through
// listServedTools instead, because a server a test built is not the one that gets served.
func listTools(t *testing.T, s *mcp.Server) ([]string, []*mcp.Tool) {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names, res.Tools
}

// The ratchet: every tool the SERVED server exposes carries a declaration. It passes today
// by CONSTRUCTION — register is the only registration path and it takes the declaration as
// an argument — and this asserts that construction actually holds end to end, on the
// catalog New's own handler offers rather than on one this test assembled. Those were the
// same thing only by convention until they were made the same call.
func TestEveryRegisteredToolDeclaresItsRisk(t *testing.T) {
	names, catalog, _ := servedCatalog(t)
	if len(names) == 0 {
		t.Fatal("the catalog is empty, so this test is measuring nothing")
	}
	if missing := UndeclaredTools(names, catalog); len(missing) > 0 {
		t.Errorf("tools registered with no risk declaration: %v. Register through register(), "+
			"which takes the declaration, rather than calling mcp.AddTool directly", missing)
	}
	// The other direction: a declaration for a tool that no longer exists keeps the
	// check green over a gap.
	for _, declared := range catalog.Names() {
		if !containsName(names, declared) {
			t.Errorf("a risk is declared for %q, which the server does not expose", declared)
		}
	}
}

// 🔴 THE NEGATIVE CONTROL. A completeness check that has only ever been run against a
// complete catalog has not been shown to fail, and a check that has not been shown to fail
// is worth nothing. This registers a tool THE LONG WAY ROUND — straight through
// mcp.AddTool, bypassing register, which is exactly the mistake a future author can make
// because it compiles and serves — and asserts the ratchet reports it.
//
// 🔑 IT IS THE ONE TEST HERE THAT BUILDS ITS OWN SERVER, AND IT HAS TO: the corruption is
// the point, and putting it on the served surface would be inflicting it on every other
// test in the package. What it proves is that UndeclaredTools goes red for an undeclared
// name; what feeds that function the SERVED names is TestEveryRegisteredToolDeclaresItsRisk
// above.
func TestTheRatchetGoesRedForAToolRegisteredWithoutADeclaration(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	c := registerTools(s, NewTools(NewGraphQLClient()))

	// The bypass, written out deliberately. Nothing about it is refused by the compiler.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_secrets_of_the_realm",
		Description: "A tool added without a risk declaration, to prove the ratchet notices.",
	}, func(context.Context, *mcp.CallToolRequest, ListDevicesInput) (*mcp.CallToolResult, ListDevicesOutput, error) {
		return nil, ListDevicesOutput{}, nil
	})

	names, _ := listTools(t, s)
	missing := UndeclaredTools(names, c)
	if len(missing) != 1 || missing[0] != "list_secrets_of_the_realm" {
		t.Fatalf("the ratchet reported %v for a catalog containing one undeclared tool; it "+
			"cannot go red, so it proves nothing about the declared ones either", missing)
	}
}

// The second negative control: a declaration that does not describe anything is refused at
// registration, loudly, rather than accepted as a placeholder.
//
// 🔴 THE PLACEHOLDER IS THE FAILURE MODE THIS REPOSITORY KEEPS HITTING. A completeness
// check satisfied by a zero value reinstates the gap with the compiler's blessing, so each
// field's empty case is exercised separately — a single "empty struct" case would pass
// while two of the three checks were missing.
func TestAnIncompleteDeclarationIsRefusedAtRegistration(t *testing.T) {
	cases := map[string]ToolRisk{
		"zero value":       {},
		"no exposure":      {Scale: ScalePage, Discloses: "something"},
		"unknown exposure": {Exposure: "vibes", Scale: ScalePage, Discloses: "something"},
		"no scale":         {Exposure: ExposureTelemetry, Discloses: "something"},
		"unknown scale":    {Exposure: ExposureTelemetry, Scale: "enormous", Discloses: "something"},
		"no disclosure":    {Exposure: ExposureTelemetry, Scale: ScalePage},
	}
	for name, risk := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("registering a tool with a %s declaration was accepted; the "+
						"declaration is then decoration, and a placeholder satisfies the "+
						"completeness check with the compiler's blessing", name)
				}
			}()
			s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
			register(s, NewCatalog(), &mcp.Tool{Name: "probe"}, risk,
				func(context.Context, *mcp.CallToolRequest, ListDevicesInput) (*mcp.CallToolResult, ListDevicesOutput, error) {
					return nil, ListDevicesOutput{}, nil
				})
		})
	}
}

// A second registration under the same name would silently replace the first
// declaration, leaving the catalog describing a tool by somebody else's risk.
func TestRegisteringTheSameToolTwiceIsRefused(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a duplicate registration was accepted; the second declaration would " +
				"silently replace the first")
		}
	}()
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	c := NewCatalog()
	risk := ToolRisk{Exposure: ExposureConfiguration, Scale: ScaleAddressed, Discloses: "x"}
	h := func(context.Context, *mcp.CallToolRequest, ListDevicesInput) (*mcp.CallToolResult, ListDevicesOutput, error) {
		return nil, ListDevicesOutput{}, nil
	}
	register(s, c, &mcp.Tool{Name: "probe"}, risk, h)
	register(s, c, &mcp.Tool{Name: "probe"}, risk, h)
}

// 🔴 THE ASSERTION THAT MAKES THE METADATA LOAD-BEARING RATHER THAN DECORATIVE. A tool
// declaring ExposurePosition must not be reachable on the general read scope: position
// re-identifies a PERSON, and the consent screen renders raw scope strings, so a scope
// that silently covered it could not be told apart from one that did not.
//
// It replaces a hard-coded `if name == "query_locations"` with a declared property, which
// is the difference between pinning today's catalog and pinning the rule: the next
// position tool is covered the moment it declares what it is.
func TestAPositionToolIsNotReachableOnTheGeneralReadScope(t *testing.T) {
	names, catalog, _ := servedCatalog(t)
	readOnly, ok := coreauth.ScopeAllowance(coreauth.ScopeReadOnly)
	if !ok {
		t.Fatal("read-only is not a defined scope")
	}
	inReadOnly := map[string]bool{}
	for _, a := range readOnly {
		inReadOnly[a] = true
	}

	sawPosition := false
	for _, name := range names {
		risk, declared := catalog.Risk(name)
		if !declared {
			continue // reported by the completeness ratchet above
		}
		position := risk.Exposure == ExposurePosition
		sawPosition = sawPosition || position
		for _, needed := range toolAuthorities[name] {
			admitted := inReadOnly[string(needed)]
			if position && admitted {
				t.Errorf("tool %q declares position exposure yet its authority %q is admitted "+
					"by the read-only ceiling %v, so an agent authorized for read-only alone "+
					"reaches it and the resource owner is never shown the choice",
					name, needed, readOnly)
			}
			if !position && !admitted {
				t.Errorf("tool %q needs %q, which read-only does not admit. Either it discloses "+
					"something that deserves its own scope — in which case say so in its "+
					"exposure — or the read-only ceiling is wrong", name, needed)
			}
		}
	}
	// Without this the loop is vacuously satisfied by a catalog that declares no position
	// tool at all, which is the state the assertion is meant to protect against reaching
	// by accident.
	if !sawPosition {
		t.Fatal("no tool declares position exposure, so this check proved nothing; if the " +
			"position tool was removed, remove this test deliberately rather than leaving " +
			"it passing over an empty set")
	}
}

// The declaration is published in the tool listing, which is what keeps it honest: a
// statement only a test reads drifts into whatever makes the test pass.
func TestTheDeclarationIsPublishedOnTheToolListing(t *testing.T) {
	_, catalog, tools := servedCatalog(t)
	for _, tool := range tools {
		risk, declared := catalog.Risk(tool.Name)
		if !declared {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not annotated read-only, which every tool on this server is",
				tool.Name)
			continue
		}
		raw, ok := tool.Meta[riskMetaKey]
		if !ok {
			t.Errorf("tool %q carries no %s metadata, so a client deciding what to authorize "+
				"cannot see what it discloses", tool.Name, riskMetaKey)
			continue
		}
		// The listing arrives as generic JSON, so it is re-read the way a client would.
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Errorf("tool %q risk metadata does not marshal: %v", tool.Name, err)
			continue
		}
		var published struct {
			Exposure  string `json:"exposure"`
			Scale     string `json:"scale"`
			Discloses string `json:"discloses"`
		}
		if err := json.Unmarshal(encoded, &published); err != nil {
			t.Errorf("tool %q risk metadata is not the published shape: %v", tool.Name, err)
			continue
		}
		if published.Exposure != string(risk.Exposure) || published.Scale != string(risk.Scale) ||
			published.Discloses != risk.Discloses {
			t.Errorf("tool %q publishes %+v, which is not what it declared (%+v)",
				tool.Name, published, risk)
		}
	}
}
