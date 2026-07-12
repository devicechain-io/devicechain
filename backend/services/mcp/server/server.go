// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/url"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName / serverVersion identify this MCP server to clients (initialize).
const (
	serverName    = "devicechain"
	serverVersion = "0.1.0"
)

// New builds the MCP server's HTTP surface (ADR-047): the MCP endpoint over
// Streamable HTTP, wrapped in the OAuth 2.1 Resource Server bearer-token
// middleware, plus the RFC 9728 protected-resource metadata handler.
//
//   - resourceID is this server's identifier (the audience tokens must be bound to).
//   - issuer is the Authorization Server that issues tokens for it.
//   - validator is the late-bound JWKS validator (nil until the readiness gate opens).
//
// It returns (mcpHandler, metadataHandler) for the caller to mount at /mcp and the
// RFC 9728 well-known path.
func New(resourceID, issuer string, validator func() *coreauth.Validator) (mcpHandler, metadataHandler http.Handler) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerTools(mcpServer, NewTools(NewGraphQLClient()))

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)

	// The RS middleware verifies the bearer (signature + audience, via the verifier)
	// AND enforces the read-only scope, and on failure emits the RFC 9728
	// WWW-Authenticate challenge pointing at the protected-resource metadata.
	protected := sdkauth.RequireBearerToken(
		NewTokenVerifier(validator, resourceID),
		&sdkauth.RequireBearerTokenOptions{
			ResourceMetadataURL: metadataURL(resourceID),
			Scopes:              []string{coreauth.ScopeReadOnly},
		},
	)(streamable)

	return protected, ProtectedResourceMetadataHandler(resourceID, issuer)
}

// registerTools wires every read tool onto the server. Kept separate so the tool
// catalog is one list as it grows across slices.
func registerTools(s *mcp.Server, t *Tools) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices in the caller's tenant (paged). Returns each device's token, name, description, external id, and device-type token. Address devices by token in follow-up tools.",
	}, t.ListDevices)
}

// metadataURL is the absolute URL of the RFC 9728 protected-resource metadata: the
// well-known path at the resource identifier's origin. Advertised in the 401
// WWW-Authenticate challenge so a client can discover the Authorization Server.
func metadataURL(resourceID string) string {
	u, err := url.Parse(resourceID)
	if err != nil {
		return resourceID + ProtectedResourceMetadataPath
	}
	origin := url.URL{Scheme: u.Scheme, Host: u.Host}
	return origin.String() + ProtectedResourceMetadataPath
}
