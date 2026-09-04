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
	// The catalog of risk declarations is not needed here: it is published on each tool's
	// own listing at registration, so nothing at runtime consults it, and it is dropped
	// rather than stashed on a field nobody reads. The ratchet gets its copy by calling
	// newServer itself, and gets the tool NAMES from the handler built below.
	mcpServer, _ := newServer()

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

// newServer builds the MCP server this process serves, together with the catalog of the
// risk declarations made while registering its tools.
//
// 🔴 IT EXISTS SO THE SERVER UNDER TEST IS THE SERVER THAT GETS SERVED. New used to
// construct its own mcp.Server inline and every ratchet in this package constructed a
// SECOND one, which made "register is the only registration path" a property of
// registerTools' SOURCE rather than of anything served: an mcp.AddTool call placed in New,
// one line after the registration, compiled, served an undeclared tool, and left every
// test green. Construction lives here now, New only wires HTTP in front of it, and the
// ratchet lists what New's own handler offers a real session — so the gap has to be
// visible from the outside to exist at all.
func newServer() (*mcp.Server, *Catalog) {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	return s, registerTools(s, NewTools(NewGraphQLClient()))
}

// registerTools wires every read tool onto the server, each with its declared risk
// metadata, and returns the catalog of those declarations.
//
// 🔴 EVERY TOOL GOES THROUGH register, NOT mcp.AddTool. That is what makes the risk
// declaration impossible to forget: it is an argument to the only registration path, not
// an entry in a second list that has to be kept in step. A tool added the long way round —
// here, or anywhere else that can reach the served server — compiles and serves, so the
// ratchet in risk_test.go closes that last gap by listing the tools New's OWN handler
// offers a real session. Its negative control adds exactly such a tool to prove the
// ratchet goes red.
func registerTools(s *mcp.Server, t *Tools) *Catalog {
	c := NewCatalog()
	register(s, c, &mcp.Tool{
		Name:        "list_devices",
		Description: "List the devices in the caller's tenant (paged). Returns each device's token, name, description, external id, and device-type token. Address devices by token in follow-up tools.",
	}, ToolRisk{
		Exposure: ExposureConfiguration, Scale: ScalePage,
		Discloses: "the full inventory of a tenant's devices, a page at a time, including any external ids that tie them to the customer's own systems",
	}, t.ListDevices)
	register(s, c, &mcp.Tool{
		Name:        "get_device",
		Description: "Look up one or more devices by token. Returns each device's token, name, description, external id, and device-type token. Unknown tokens are simply omitted from the result (not an error), so compare the returned tokens against the ones requested.",
	}, ToolRisk{
		Exposure: ExposureConfiguration, Scale: ScaleAddressed,
		Discloses: "the registry entry for devices the caller already names, and — because unknown tokens are omitted rather than refused — whether a guessed token exists",
	}, t.GetDevice)
	register(s, c, &mcp.Tool{
		Name:        "get_device_state",
		Description: "Read the live last-known connectivity state of one or more devices by token: whether active, last connect/disconnect/activity times (RFC3339), the inactivity timeout in seconds, and `presenceSource`. A device with no state yet (never reported) is omitted from the result. `presenceSource` says WHERE `active` came from, and the two values do not support the same conclusion. ASSERTED means the device's transport reported its connection state directly (a broker session opening or dropping), so `active` is authoritative: false means the device is known to be disconnected. INFERRED means the state was derived from data arriving — or not arriving within the inactivity timeout — so `active` false means only that nothing has been heard recently, NOT that the device is known to be down; a healthy device that reports less often than the timeout reads inactive. Describe an inferred inactive device as not reporting, never as offline or down, and say which of the two you are reading whenever the difference could change what someone does. Treat any `presenceSource` that is not exactly ASSERTED, including an empty one, as inferred.",
	}, ToolRisk{
		Exposure: ExposureOperational, Scale: ScaleAddressed,
		Discloses: "whether named devices are connected and when they were last heard from, which for a single-device installation is a proxy for whether its site is occupied or running",
	}, t.GetDeviceState)
	register(s, c, &mcp.Tool{
		Name:        "get_latest_measurements",
		Description: "Read the latest (last-known) value of every metric for a device, with its unit, data type, and time. Prefer this over querying raw history for a current snapshot.",
	}, ToolRisk{
		Exposure: ExposureTelemetry, Scale: ScaleAddressed,
		Discloses: "the current value of every metric a named device reports — the tenant's live product data for that device, though only its present state and not its history",
	}, t.GetLatestMeasurements)
	register(s, c, &mcp.Tool{
		Name:        "get_device_capabilities",
		Description: "Report what a device can measure and what it can be told to do. `commands` is the device's PUBLISHED command vocabulary — the platform accepts exactly these, with `parameterSchema` describing each one's arguments. `commandsConstrained` says whether the vocabulary is enforced at all: when it is false the platform accepts ANY command key and `commands` is empty, so an empty list means the vocabulary is OPEN, not that the device takes no commands. `metrics` are the profile's DRAFT metric definitions (key, name, unit, data type) and may name metrics the device does not yet report; `activeVersion` is the published profile version the device resolves, and when it is null the profile has never been published, so treat the listed metrics as not yet active.",
	}, ToolRisk{
		Exposure: ExposureConfiguration, Scale: ScaleAddressed,
		Discloses: "a named device's metric definitions and its published command vocabulary — what the platform would accept being told to do to it, though this server can never issue any of it",
	}, t.GetDeviceCapabilities)
	register(s, c, &mcp.Tool{
		Name:        "query_measurements",
		Description: "Query raw measurement history for a device over an optional time window (paged). For trends over a window prefer aggregate_measurements — it returns far fewer rows for the same insight.",
	}, ToolRisk{
		Exposure: ExposureTelemetry, Scale: ScalePage,
		Discloses: "a device's raw reading history, a page at a time, from which an operating pattern over time can be reconstructed rather than only a current state",
	}, t.QueryMeasurements)
	register(s, c, &mcp.Tool{
		Name:        "aggregate_measurements",
		Description: "Return time-bucketed avg/min/max/sum/count of a device's measurements over a window (intervalSeconds sets the bucket width, e.g. 3600 for hourly). The token-efficient way to read trends — prefer this over query_measurements for anything but a small exact-value lookup. Always bound the query with startTime/endTime: without a window it returns a bucket for the entire history.",
	}, ToolRisk{
		Exposure: ExposureTelemetry, Scale: ScaleWindow,
		Discloses: "a statistical summary of a device's readings that, called with no window, spans everything the platform still retains — the one tool here whose single call is bounded by the retention rather than by a page",
	}, t.AggregateMeasurements)
	register(s, c, &mcp.Tool{
		Name:        "query_locations",
		Description: "Query a device's reported positions over an optional time window (paged). Results are NEWEST FIRST, so the first one is the device's last known position — to answer only \"where is it now\", call with pageSize 1 and no window. Each position carries latitude/longitude and, when the receiver reported them, elevation (metres), accuracy (metres, horizontal), speed (metres per second) and heading (degrees clockwise from true north). A field that is absent was not reported: treat it as unknown, never as zero — a missing speed is not a stationary device and a missing heading is not due north. Reading positions needs BOTH the separate `location` OAuth scope (which the client must have been authorized for, alongside `read-only`) and the caller's own location permission, so this can be refused for a caller whose other read tools all work.",
	}, ToolRisk{
		Exposure: ExposurePosition, Scale: ScalePage,
		Discloses: "where a device has physically been over time, which for anything carried by a person is a record of that person's movements rather than of equipment",
	}, t.QueryLocations)
	register(s, c, &mcp.Tool{
		Name:        "list_alarms",
		Description: "List alarms in the caller's tenant (paged), optionally filtered by originating device token, state, severity, alarm key, or acknowledged flag. Returns each alarm's token, key, metric, state, severity, acknowledged flag, raised/cleared times, last value, and message.",
	}, ToolRisk{
		Exposure: ExposureOperational, Scale: ScalePage,
		Discloses: "a tenant-wide list of what has been going wrong and where, a page at a time, including the message and triggering value on each alarm",
	}, t.ListAlarms)
	register(s, c, &mcp.Tool{
		Name:        "get_alarm",
		Description: "Look up one or more alarms by token, returning full alarm detail.",
	}, ToolRisk{
		Exposure: ExposureOperational, Scale: ScaleAddressed,
		Discloses: "full detail of alarms the caller already names, including the message and the value that raised each one",
	}, t.GetAlarm)
	register(s, c, &mcp.Tool{
		Name:        "list_commands",
		Description: "List dispatched commands in the caller's tenant (paged), optionally filtered by device token or status. Returns each command's token, device, name, status, and delivery-lifecycle timestamps (payloads are omitted).",
	}, ToolRisk{
		Exposure: ExposureOperational, Scale: ScalePage,
		Discloses: "what a tenant has been telling its fleet to do and whether each instruction landed — the command names and lifecycle, though never the payloads",
	}, t.ListCommands)
	return c
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
