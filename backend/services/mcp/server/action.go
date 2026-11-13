// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Impact string

const (
	ImpactNormal      Impact = "normal"
	ImpactDisruptive  Impact = "disruptive"
	ImpactDestructive Impact = "destructive"
)

// ActionRisk declares a proposal's effect, or a ledger read's disclosure.
type ActionRisk struct {
	Impact     Impact
	Reversible bool
	Discloses  string
	ReadOnly   bool
}

type options struct{ actuation bool }

// Option configures optional MCP capabilities.
type Option func(*options)

// WithActuation enables Preview proposal tools; each caller still needs an act scope.
func WithActuation(enabled bool) Option { return func(o *options) { o.actuation = enabled } }

func (r ActionRisk) validate(name string) error {
	switch r.Impact {
	case ImpactNormal, ImpactDisruptive, ImpactDestructive:
	default:
		return fmt.Errorf("invalid action impact for %q", name)
	}
	if strings.TrimSpace(r.Discloses) == "" {
		return fmt.Errorf("missing action disclosure for %q", name)
	}
	return nil
}

// An empty required set means any recognised act scope, never an arbitrary act: prefix.
func carriesActScope(extra *mcp.RequestExtra, required []string) bool {
	if extra == nil || extra.TokenInfo == nil {
		return false
	}
	for _, scope := range extra.TokenInfo.Scopes {
		if !coreauth.IsActScope(scope) {
			continue
		}
		if len(required) == 0 {
			return true
		}
		for _, want := range required {
			if want == scope {
				return true
			}
		}
	}
	return false
}

const actScopeRefusal = "this tool requires an act scope the token does not carry"

func registerAction[In, Out any](s *mcp.Server, c *Catalog, tool *mcp.Tool, required []string, risk ActionRisk, enabled bool, h mcp.ToolHandlerFor[In, Out]) {
	if err := risk.validate(tool.Name); err != nil {
		panic("mcp: " + err.Error())
	}
	for _, scope := range required {
		if !coreauth.IsActScope(scope) {
			panic("mcp: action requires an invalid scope")
		}
	}
	if !enabled {
		return
	}
	if _, exists := c.decls[tool.Name]; exists {
		panic("mcp: duplicate tool " + tool.Name)
	}
	c.actions[tool.Name] = append([]string{}, required...)
	c.actionRisks[tool.Name] = risk
	if !strings.Contains(tool.Description, untrustedNotice) {
		tool.Description = strings.TrimSpace(tool.Description) + " " + untrustedNotice
	}
	destructive := false
	tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: risk.ReadOnly, DestructiveHint: &destructive, IdempotentHint: risk.ReadOnly}
	tool.Meta = mcp.Meta{riskMetaKey: map[string]any{"impact": string(risk.Impact), "reversible": risk.Reversible, "discloses": risk.Discloses, "readOnly": risk.ReadOnly}}
	finishRegistration(s, c, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		if !carriesActScope(req.Extra, required) {
			var out Out
			return nil, out, fmt.Errorf("%s", markedErrorText(actScopeRefusal))
		}
		return h(ctx, req, in)
	})
}

// Installed inside auditing: a refused call is still recorded by the outer middleware.
func filterActionScopes(c *Catalog) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if call, ok := req.(*mcp.CallToolRequest); ok && method == "tools/call" && call.Params != nil {
				if required, action := c.actions[call.Params.Name]; action && !carriesActScope(call.Extra, required) {
					return nil, fmt.Errorf("%s", markedErrorText(actScopeRefusal))
				}
			}
			result, err := next(ctx, method, req)
			if method == "tools/list" && err == nil {
				if list, ok := result.(*mcp.ListToolsResult); ok {
					visible := make([]*mcp.Tool, 0, len(list.Tools))
					for _, tool := range list.Tools {
						required, action := c.actions[tool.Name]
						if !action || carriesActScope(req.GetExtra(), required) {
							visible = append(visible, tool)
						}
					}
					copy := *list
					copy.Tools = visible
					result = &copy
				}
			}
			return result, err
		}
	}
}
