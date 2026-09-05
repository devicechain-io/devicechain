---
title: AI Access (MCP)
---

# AI Access (MCP)

AI assistants — Claude Desktop and Claude Code, Cursor, VS Code — can operate a DeviceChain tenant on a user's behalf through a **Model Context Protocol (MCP)** server. An LLM client connects, discovers a set of tools, and calls them to answer questions about your fleet: *"which devices in Building 3 haven't reported in the last hour?"*, *"summarize today's alarms for the cold-storage assets"*, *"what's the latest temperature on thermostat T-114?"*

DeviceChain's MCP server is built around a single principle: **an AI agent can never do more than the person who authorized it.** Rather than a broad, over-permissioned gateway, it is a thin, curated, read-only layer over the platform's existing GraphQL API, carrying the signed-in user's own tenant-scoped token.

:::note Status
**Available today (read-only):** an opt-in `mcp` service exposing eleven curated read tools, fronted by a full OAuth 2.1 authorization server on `user-management` (authorization-code flow with PKCE, RFC 8414 metadata, refresh-token rotation, RFC 8707 audience binding). **Planned:** write tools (send command, acknowledge/clear alarm) behind an elevated scope and a mandatory human-in-the-loop confirmation; and dynamic client registration (RFC 7591) — today clients are registered by an administrator. This repository is the source of truth for what currently builds.
:::

## What an assistant can do

The server exposes eleven **read** tools. Each one is a query against the same GraphQL API the console uses, run under the caller's token — so a tool returns exactly what that user, in that tenant, is allowed to see, and nothing more.

**Devices**

- `list_devices` — list devices, with filtering.
- `get_device` — a single device's details.
- `get_device_capabilities` — what a device can measure, and the **published** commands it accepts (with each command's parameter schema).

**Live state & telemetry**

- `get_device_state` — the current last-known state of a device, including whether that state was **reported by the transport** or **inferred from silence**. The distinction changes what "not active" means: reported means the device is known to be disconnected, inferred means only that nothing has arrived recently — which is also what a healthy device on a slow reporting interval looks like.
- `get_latest_measurements` — the most recent value per measurement.
- `query_measurements` — raw time-series readings over a time range.
- `aggregate_measurements` — bucketed aggregates (min/max/avg and the like) over a range.

**Position**

- `query_locations` — a device's reported positions over an optional time window, paged and bounded. Results are **newest first**, so the first one is the device's last known position; asking for a single result with no window answers "where is it now". Each position carries latitude and longitude and, when the receiver reported them, elevation, accuracy, speed and heading — a field that is absent was not reported, and means unknown rather than zero. Reading positions needs the separate **location** permission, which is deliberately not part of the read-only viewer baseline, so this one tool can be refused for a caller whose other read tools all work.

**Alarms**

- `list_alarms` — alarms, with filtering by state and entity.
- `get_alarm` — a single alarm's details.

**Commands**

- `list_commands` — the commands issued to a device and their status.

There is **no** generic "run this GraphQL query" tool, and sensitive reads — credentials, the audit trail, notification recipients, provisioning secrets — are deliberately excluded from the tool set.

## The security model

MCP is becoming a standard way to give AI assistants real capabilities, and the risk is that a careless implementation hands an agent a powerful, broadly-scoped key. DeviceChain's server is designed so that structurally cannot happen.

- **It carries the user's token — never a service token.** The MCP server holds no privileged platform credential. Every tool call forwards the *caller's* validated, tenant-scoped JWT to the underlying GraphQL service, so the agent's reach is exactly the user's reach. (Handing an AI a service identity would create a "confused deputy" that could act across tenants — the one thing this design refuses.)
- **The tenant is pinned at grant time, not passed as a parameter.** Which tenant the token can act in is decided during authorization, then baked into the token. No tool takes a "tenant" argument an agent could change.
- **Tokens are audience-bound.** An access token issued for the MCP server is stamped with that server as its intended audience (RFC 8707) and rejected anywhere else — a token minted for one resource can't be replayed against another.
- **Read-only, and curated.** The whole tool set is queries. There is no write path, no generic query escape hatch, and no exposure of sensitive objects.
- **Every call is authenticated and re-checked.** The server validates the bearer token against `user-management`'s public keys on each request and enforces a read-only scope; the underlying GraphQL service independently re-applies the same tenant and role checks the console gets.

The result: connect an assistant, and it can *read devices, state, measurements, and alarms* for your tenant — and it physically cannot reach another tenant, mutate anything, or run an arbitrary query.

## How a client connects

The MCP server is an **OAuth 2.1 resource server**, and `user-management` is its **authorization server** — so connecting a client is a standard OAuth flow, not a bespoke key exchange:

1. The client discovers the server's requirements from its protected-resource metadata (RFC 9728), then finds the authorization server from *its* metadata (RFC 8414). Both documents live at a well-known path built by inserting the well-known segment **between** the host and the identifier's path — so on an instance at `iot.example.com` they are `/.well-known/oauth-protected-resource/api/mcp` and `/.well-known/oauth-authorization-server/api/user-management`.
2. The user is sent through the **authorization-code flow with PKCE** (`/oauth/authorize`): they sign in, choose the tenant to grant, and consent — all server-rendered, no shared secret.
3. The client exchanges the code for a tenant-scoped access token at `/oauth/token`, and refreshes it as needed (refresh tokens are single-use and rotated).
4. The client calls MCP tools with that token; each call runs under the user's own permissions.

Clients are **registered by an administrator** (through the admin API) rather than self-registering, so an operator controls which applications may request access and with what redirect URIs.

### Where to point a client {#where-to-point-a-client}

There is one URL to configure, and it is the instance's public host plus `/api/mcp`:

```
https://<your-instance-host>/api/mcp
```

That single string is three things at once, which is why it is the only one you need:

- the **endpoint** the client POSTs its MCP requests to;
- the **resource identifier** the client sends as the `resource` parameter when it asks for a token, and that the token is stamped with as its audience;
- the **starting point for discovery** — everything else is derived from it.

Nothing else needs to be entered by hand. A client that receives the `401` from that endpoint reads the metadata location out of the response's `WWW-Authenticate` header, follows it, and from that document learns where the authorization server is and asks *it* for its own metadata. Discovery is those three requests, and you can walk them by hand before pointing a client at anything:

```bash
# 1. The endpoint answers 401 and names its metadata document.
curl -i -X POST https://<your-instance-host>/api/mcp

# 2. That document names the authorization server.
curl https://<your-instance-host>/.well-known/oauth-protected-resource/api/mcp

# 3. The authorization server describes where to log in and get a token.
curl https://<your-instance-host>/.well-known/oauth-authorization-server/api/user-management
```

The well-known paths look odd the first time you see them: the well-known segment goes **between** the host and the rest of the path rather than after it. That is the location the standards define for an identifier that carries a path, so it is what a client builds on its own. For the second document, the more intuitive-looking `https://<host>/api/mcp/.well-known/oauth-protected-resource` serves the same thing, for clients that construct it that way instead.

All three requests are unauthenticated — discovery is public by design and returns no secrets. Request 3 only answers once the authorization server is switched on; see [below](#limits-and-boundaries) for why that is a separate step.

:::caution Run exactly one replica
The MCP server keeps each client's protocol session in memory, on the pod that created it. Sessions are not shared between pods and there is no session affinity, so a second replica means roughly half of every client's requests arrive at a pod that has never heard of its session and are refused. The failure is intermittent and its message does not mention scaling, so it reads as a client bug. Installing with more than one replica for this area is refused outright rather than left to be discovered.
:::

## Limits and boundaries {#limits-and-boundaries}

Some of these are unfinished work and some are decisions. They are worth telling apart, so each one says which.

**Deliberate, and not scheduled to change:**

- **No writes.** Sending a command or acknowledging an alarm through MCP is planned, but only behind an elevated scope *and* an explicit human confirmation — an assistant will never silently actuate a device.
- **No cross-tenant access.** The token is scoped to one tenant, chosen by the user at grant time. Tenancy is never a tool parameter, so there is no argument an agent can vary to reach across.
- **No arbitrary queries.** Only the curated tool set is reachable; there is no `run_graphql`.
- **No service credential is wired into any code path it runs.** This is stronger than "it does not use a service token": there is no code path in the MCP server that reaches for a credential of its own, so there is nothing for a confused-deputy attack to borrow. Every downstream read goes out under the caller's own token, and an agent with no permission to read something gets the same refusal a person would. (Its pod mounts the instance configuration like every other service's does — that is deployment plumbing, not something the server uses.)
- **Command payloads are not returned.** `list_commands` gives you a command's name, status and timings; what was sent to the device stays out of the agent's context.

**Bounds you will hit before you hit anything else:**

- Results are paged at 25 by default and 100 at most, and a multi-device lookup takes at most 50 tokens per call. An agent surveying a large fleet pages through it.
- A single downstream response the server will read is capped at 8 MiB — that is a bound on what it fetches from the platform's own APIs, not a limit it advertises to the agent. A session idles out after 30 minutes.

**Enabling the service is not enough to use it.** There are two independent switches, and turning on only the first is the common way to end up with a server that answers and cannot be reached:

1. The `mcp` functional area is not in a default deployment; an operator enables it explicitly.
2. The authorization server on `user-management` is itself off until an issuer URL is configured. Until then, `mcp` starts and serves its metadata, and **no client can obtain a token**. It is a separate switch on purpose: setting the issuer changes a claim on every token the instance mints, not just the ones MCP uses.

## Related

- **[Multi-Tenancy](./multi-tenancy.md)** — how tenant isolation is enforced, which is what the MCP token rides on.
- **[Architecture](./architecture.md)** — where the `mcp` service sits, and the [secret handling](./architecture.md#secret-handling) model for credentials.
- **[GraphQL API](../reference/graphql-api.md)** — the API the MCP tools front.
