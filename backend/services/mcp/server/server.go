// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/url"
	"time"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName / serverVersion identify this MCP server to clients (initialize).
const (
	serverName    = "devicechain"
	serverVersion = "0.1.0"
)

// sessionTimeout bounds idle MCP sessions. The SDK never reaps sessions when this
// is zero, so an authenticated client that repeatedly re-initializes (exactly what
// long-lived LLM agents do) would grow the in-memory session map without bound and
// OOM the pod. Reaping idle sessions caps that.
const sessionTimeout = 30 * time.Minute

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

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout},
	)

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
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_device",
		Description: "Look up one or more devices by token. Returns each device's token, name, description, external id, and device-type token. Unknown tokens are simply omitted from the result (not an error), so compare the returned tokens against the ones requested.",
	}, t.GetDevice)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_device_state",
		Description: "Read the live last-known connectivity state of one or more devices by token: whether active, last connect/disconnect/activity times (RFC3339), the inactivity timeout in seconds, and `presenceSource`. A device with no state yet (never reported) is omitted from the result. `presenceSource` says WHERE `active` came from, and the two values do not support the same conclusion. ASSERTED means the device's transport reported its connection state directly (a broker session opening or dropping), so `active` is authoritative: false means the device is known to be disconnected. INFERRED means the state was derived from data arriving — or not arriving within the inactivity timeout — so `active` false means only that nothing has been heard recently, NOT that the device is known to be down; a healthy device that reports less often than the timeout reads inactive. Describe an inferred inactive device as not reporting, never as offline or down, and say which of the two you are reading whenever the difference could change what someone does. Treat any `presenceSource` that is not exactly ASSERTED, including an empty one, as inferred.",
	}, t.GetDeviceState)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_latest_measurements",
		Description: "Read the latest (last-known) value of every metric for a device, with its unit, data type, and time. Prefer this over querying raw history for a current snapshot.",
	}, t.GetLatestMeasurements)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_device_capabilities",
		Description: "Report what a device can measure and what it can be told to do. `commands` is the device's PUBLISHED command vocabulary — the platform accepts exactly these, with `parameterSchema` describing each one's arguments. `commandsConstrained` says whether the vocabulary is enforced at all: when it is false the platform accepts ANY command key and `commands` is empty, so an empty list means the vocabulary is OPEN, not that the device takes no commands. `metrics` are the profile's DRAFT metric definitions (key, name, unit, data type) and may name metrics the device does not yet report; `activeVersion` is the published profile version the device resolves, and when it is null the profile has never been published, so treat the listed metrics as not yet active.",
	}, t.GetDeviceCapabilities)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "query_measurements",
		Description: "Query raw measurement history for a device over an optional time window (paged). For trends over a window prefer aggregate_measurements — it returns far fewer rows for the same insight.",
	}, t.QueryMeasurements)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "aggregate_measurements",
		Description: "Return time-bucketed avg/min/max/sum/count of a device's measurements over a window (intervalSeconds sets the bucket width, e.g. 3600 for hourly). The token-efficient way to read trends — prefer this over query_measurements for anything but a small exact-value lookup. Always bound the query with startTime/endTime: without a window it returns a bucket for the entire history.",
	}, t.AggregateMeasurements)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "query_locations",
		Description: "Query a device's reported positions over an optional time window (paged). Results are NEWEST FIRST, so the first one is the device's last known position — to answer only \"where is it now\", call with pageSize 1 and no window. Each position carries latitude/longitude and, when the receiver reported them, elevation (metres), accuracy (metres, horizontal), speed (metres per second) and heading (degrees clockwise from true north). A field that is absent was not reported: treat it as unknown, never as zero — a missing speed is not a stationary device and a missing heading is not due north. Reading positions needs BOTH the separate `location` OAuth scope (which the client must have been authorized for, alongside `read-only`) and the caller's own location permission, so this can be refused for a caller whose other read tools all work.",
	}, t.QueryLocations)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_alarms",
		Description: "List alarms in the caller's tenant (paged), optionally filtered by originating device token, state, severity, alarm key, or acknowledged flag. Returns each alarm's token, key, metric, state, severity, acknowledged flag, raised/cleared times, last value, and message.",
	}, t.ListAlarms)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_alarm",
		Description: "Look up one or more alarms by token, returning full alarm detail.",
	}, t.GetAlarm)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_commands",
		Description: "List dispatched commands in the caller's tenant (paged), optionally filtered by device token or status. Returns each command's token, device, name, status, and delivery-lifecycle timestamps (payloads are omitted).",
	}, t.ListCommands)
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
