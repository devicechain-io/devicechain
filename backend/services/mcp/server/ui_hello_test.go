// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// uiSession connects to the served handler as a client that does, or does not, declare
// the MCP Apps extension the way a host does.
func uiSession(t *testing.T, declare bool) *mcp.ClientSession {
	t.Helper()
	iss, validator := mustIssuerValidator(t)
	handler, _ := New(testResource, "https://as.example.com", validator, testClient("http://unused.invalid"), DefaultLimits())
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
		"read-only", []string{testResource}, false, "mcp", "j-ui")
	if err != nil {
		t.Fatal(err)
	}
	var opts *mcp.ClientOptions
	if declare {
		caps := &mcp.ClientCapabilities{}
		caps.AddExtension(uiExtension, map[string]any{"mimeTypes": []any{uiMIMEType}})
		opts = &mcp.ClientOptions{Capabilities: caps}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: tok.Token}},
			MaxRetries: -1, DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestHelloView_ServerAdvertisesTheExtension(t *testing.T) {
	for _, declare := range []bool{true, false} {
		cs := uiSession(t, declare)
		if _, ok := cs.InitializeResult().Capabilities.Extensions[uiExtension]; !ok {
			t.Errorf("declare=%v: initialize result does not advertise %s", declare, uiExtension)
		}
	}
}

func TestHelloView_NegotiatedSessionSeesToolResourceAndResult(t *testing.T) {
	cs := uiSession(t, true)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var hello *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "hello_view" {
			hello = tool
		}
	}
	if hello == nil {
		t.Fatal("hello_view is not listed for a client that negotiated the extension")
	}
	ui, _ := hello.Meta["ui"].(map[string]any)
	if ui["resourceUri"] != helloViewURI {
		t.Errorf("_meta.ui.resourceUri = %v, want %s", ui["resourceUri"], helloViewURI)
	}
	if _, flat := hello.Meta["ui/resourceUri"]; flat {
		t.Error("the deprecated flat _meta key must not be used")
	}

	rs, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, r := range rs.Resources {
		if r.URI == helloViewURI {
			listed = true
			if r.MIMEType != uiMIMEType {
				t.Errorf("listed MIME type = %q", r.MIMEType)
			}
		}
	}
	if !listed {
		t.Error("ui resource not listed for a negotiated client")
	}

	read, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: helloViewURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Contents) != 1 || read.Contents[0].MIMEType != uiMIMEType || read.Contents[0].Text != helloViewHTML {
		t.Errorf("resources/read did not return the embedded page as %s", uiMIMEType)
	}
	if len(read.Contents[0].Meta) != 0 {
		t.Errorf("the view declares no CSP or permissions; got _meta %v", read.Contents[0].Meta)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hello_view"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("hello_view errored: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("no text content for hosts that render nothing")
	}
	if txt, ok := res.Content[0].(*mcp.TextContent); !ok || !strings.Contains(txt.Text, "acme") {
		t.Errorf("text fallback = %+v", res.Content[0])
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["tenant"] != "acme" {
		t.Errorf("structuredContent = %v, want tenant acme", res.StructuredContent)
	}
}

func TestHelloView_UnnegotiatedSessionSeesNothingOfIt(t *testing.T) {
	cs := uiSession(t, false)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("the ordinary catalog vanished; only the UI tool should be withheld")
	}
	for _, tool := range tools.Tools {
		if tool.Name == "hello_view" {
			t.Error("hello_view listed to a client that did not negotiate the extension")
		}
		if _, ui := tool.Meta["ui"]; ui {
			t.Errorf("%s carries _meta.ui for a client that did not negotiate", tool.Name)
		}
	}
	rs, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs.Resources {
		if strings.HasPrefix(r.URI, "ui://") {
			t.Errorf("%s listed to a client that did not negotiate", r.URI)
		}
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: helloViewURI}); err == nil {
		t.Error("the view was readable without negotiation")
	}
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hello_view"}); err == nil && !res.IsError {
		t.Error("hello_view answered without negotiation")
	}
}

// A page whose network policy is the host default must name no origin of any kind, load
// nothing, and write the tenant with textContent only.
func TestHelloViewHTML_NoExternalOriginsAndNoInnerHTML(t *testing.T) {
	for _, bad := range []string{
		"http:", "https:", "ws:", "wss:", "//", "src=", "href=", "url(", "@import",
		"<link", "<iframe", "<img", "<object", "<embed", "fetch(", "XMLHttpRequest",
		"WebSocket", "importScripts", "eval(", "new Function", "innerHTML", "outerHTML",
		"insertAdjacentHTML", "document.write",
	} {
		if strings.Contains(helloViewHTML, bad) {
			t.Errorf("hello.html contains %q", bad)
		}
	}
	if n := len(regexp.MustCompile(`<script`).FindAllString(helloViewHTML, -1)); n != 1 {
		t.Errorf("want exactly one inline script, got %d", n)
	}
	if !strings.Contains(helloViewHTML, `$("tenant").textContent`) {
		t.Error("the tenant must be written with textContent")
	}
}

// withoutUITools drops the tools a client sees only after negotiating the MCP Apps
// extension, so the catalog-versus-served ratchets (which connect without declaring it)
// compare like with like.
func withoutUITools(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "hello_view" {
			out = append(out, n)
		}
	}
	return out
}
