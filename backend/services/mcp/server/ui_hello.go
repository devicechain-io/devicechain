// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SPIKE (throwaway): the smallest MCP Apps surface, to check live in a host. One tool,
// hello_view, names a ui:// HTML resource; the page shows the caller's tenant and calls
// back list_devices for a count. Nothing here holds authority: the view's call goes
// through the host to the same tools/call door, under the caller's own token.

const (
	// uiExtension is the MCP Apps extension identifier a host declares under
	// capabilities.extensions in initialize, and a server may echo in its own.
	uiExtension = "io.modelcontextprotocol/ui"
	// uiMIMEType is the resource type a host that renders views declares support for.
	uiMIMEType = "text/html;profile=mcp-app"
	// helloViewURI is the one view this spike serves.
	helloViewURI = "ui://devicechain/hello"
)

//go:embed ui/hello.html
var helloViewHTML string

// uiNegotiated reports whether the session's client declared the MCP Apps extension
// with the HTML-app MIME type. A nil session is not negotiated: fail closed.
func uiNegotiated(ss *mcp.ServerSession) bool {
	if ss == nil {
		return false
	}
	p := ss.InitializeParams()
	if p == nil || p.Capabilities == nil {
		return false
	}
	settings, ok := p.Capabilities.Extensions[uiExtension].(map[string]any)
	if !ok {
		return false
	}
	types, _ := settings["mimeTypes"].([]any)
	for _, t := range types {
		if s, ok := t.(string); ok && s == uiMIMEType {
			return true
		}
	}
	return false
}

// HelloViewInput is the hello_view tool input: none.
type HelloViewInput struct{}

// HelloViewOutput is what the view renders. Tenant is the caller's tenant token, taken
// from the verified bearer token, not from any text a device or person wrote.
type HelloViewOutput struct {
	Tenant string `json:"tenant" prov:"id"`
}

// HelloView returns the caller's tenant for the hello view. A session that did not
// negotiate the extension is refused rather than given a result nothing can render.
func (t *Tools) HelloView(_ context.Context, req *mcp.CallToolRequest, _ HelloViewInput) (*mcp.CallToolResult, HelloViewOutput, error) {
	if !uiNegotiated(req.Session) {
		return nil, HelloViewOutput{}, fmt.Errorf("this client did not negotiate the %s extension", uiExtension)
	}
	_, tenant, err := callerToken(req)
	if err != nil {
		return nil, HelloViewOutput{}, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Hello view for tenant " + tenant + "."}},
	}, HelloViewOutput{Tenant: tenant}, nil
}

// registerHelloView adds the tool (through register, so it carries a risk declaration
// like every other) and the ui:// resource it names.
func registerHelloView(s *mcp.Server, c *Catalog, t *Tools) {
	register(s, c, &mcp.Tool{
		Name:        "hello_view",
		Description: "Show a small interactive view of the caller's tenant and device count, in a client that renders MCP Apps. Spike: not part of the supported catalog.",
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": helloViewURI}},
	}, ToolRisk{
		Exposure: ExposureConfiguration, Scale: ScaleAddressed,
		Discloses: "the caller's own tenant identifier, which the caller's token already carries",
	}, t.HelloView)
	s.AddResource(&mcp.Resource{
		URI:         helloViewURI,
		Name:        "hello-view",
		Description: "Spike view: tenant and device count.",
		MIMEType:    uiMIMEType,
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if req.Params == nil || req.Params.URI != helloViewURI {
			return nil, mcp.ResourceNotFoundError(helloViewURI)
		}
		// No _meta.ui.csp: the host's default (no external origin, connect-src none)
		// is exactly what this page needs.
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: helloViewURI, MIMEType: uiMIMEType, Text: helloViewHTML,
		}}}, nil
	})
}

// gateUI withholds everything UI from a session that did not negotiate the extension:
// tools naming a ui:// resource are dropped from tools/list, ui:// resources from
// resources/list, and a resources/read of a ui:// URI is not found.
func gateUI(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		ss, _ := req.GetSession().(*mcp.ServerSession)
		if uiNegotiated(ss) {
			return next(ctx, method, req)
		}
		if method == "resources/read" {
			if p, ok := req.GetParams().(*mcp.ReadResourceParams); ok && p != nil && strings.HasPrefix(p.URI, "ui://") {
				return nil, mcp.ResourceNotFoundError(p.URI)
			}
		}
		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}
		switch r := res.(type) {
		case *mcp.ListToolsResult:
			kept := make([]*mcp.Tool, 0, len(r.Tools))
			for _, tool := range r.Tools {
				if _, ui := tool.Meta["ui"]; !ui {
					kept = append(kept, tool)
				}
			}
			r.Tools = kept
		case *mcp.ListResourcesResult:
			kept := make([]*mcp.Resource, 0, len(r.Resources))
			for _, rs := range r.Resources {
				if !strings.HasPrefix(rs.URI, "ui://") {
					kept = append(kept, rs)
				}
			}
			r.Resources = kept
		}
		return res, nil
	}
}
