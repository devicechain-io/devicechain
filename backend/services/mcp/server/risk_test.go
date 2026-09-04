// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// liveCatalog builds a real server through the real registration path and asks it, over a
// real MCP session, what it exposes — returning the tool names, the declarations made at
// registration, and the listed tools themselves.
//
// 🔑 IT ASKS THE SERVER RATHER THAN READING registerTools' SOURCE. The catalog a client
// sees is the only thing any of these assertions are about.
func liveCatalog(t *testing.T) ([]string, *Catalog, []*mcp.Tool) {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	c := registerTools(s, NewTools(NewGraphQLClient()))
	names, tools := listTools(t, s)
	return names, c, tools
}

// listTools connects a client to s and returns the catalog it is offered.
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

// The ratchet: every tool the server exposes carries a declaration. It passes today by
// CONSTRUCTION — register is the only registration path and it takes the declaration as
// an argument — and this asserts that construction actually holds end to end.
func TestEveryRegisteredToolDeclaresItsRisk(t *testing.T) {
	names, catalog, _ := liveCatalog(t)
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
	names, catalog, _ := liveCatalog(t)
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
	_, catalog, tools := liveCatalog(t)
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
