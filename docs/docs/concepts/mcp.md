---
sidebar_position: 7
title: AI Access (MCP)
---

# AI Access (MCP)

AI assistants — Claude Desktop and Claude Code, Cursor, VS Code — can operate a DeviceChain tenant on a user's behalf through a **Model Context Protocol (MCP)** server. An LLM client connects, discovers a set of tools, and calls them to answer questions about your fleet: *"which devices in Building 3 haven't reported in the last hour?"*, *"summarize today's alarms for the cold-storage assets"*, *"what's the latest temperature on thermostat T-114?"*

DeviceChain's MCP server is built around a single principle: **an AI agent can never do more than the person who authorized it.** Rather than a broad, over-permissioned gateway, it is a thin, curated, read-only layer over the platform's existing GraphQL API, carrying the signed-in user's own tenant-scoped token.

:::note Status
**Available today (read-only):** an opt-in `mcp` service exposing ten curated read tools, fronted by a full OAuth 2.1 authorization server on `user-management` (authorization-code flow with PKCE, RFC 8414 metadata, refresh-token rotation, RFC 8707 audience binding). **Planned:** write tools (send command, acknowledge/clear alarm) behind an elevated scope and a mandatory human-in-the-loop confirmation; and dynamic client registration (RFC 7591) — today clients are registered by an administrator. This repository is the source of truth for what currently builds.
:::

## What an assistant can do

The server exposes ten **read** tools. Each one is a query against the same GraphQL API the console uses, run under the caller's token — so a tool returns exactly what that user, in that tenant, is allowed to see, and nothing more.

**Devices**

- `list_devices` — list devices, with filtering.
- `get_device` — a single device's details.
- `get_device_capabilities` — what a device can measure, and the **published** commands it accepts (with each command's parameter schema).

**Live state & telemetry**

- `get_device_state` — the current last-known state of a device.
- `get_latest_measurements` — the most recent value per measurement.
- `query_measurements` — raw time-series readings over a time range.
- `aggregate_measurements` — bucketed aggregates (min/max/avg and the like) over a range.

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

1. The client discovers the server's requirements from its protected-resource metadata (RFC 9728) and finds the authorization server from its metadata (RFC 8414, at `/.well-known/oauth-authorization-server`).
2. The user is sent through the **authorization-code flow with PKCE** (`/oauth/authorize`): they sign in, choose the tenant to grant, and consent — all server-rendered, no shared secret.
3. The client exchanges the code for a tenant-scoped access token at `/oauth/token`, and refreshes it as needed (refresh tokens are single-use and rotated).
4. The client calls MCP tools with that token; each call runs under the user's own permissions.

Clients are **registered by an administrator** (through the admin API) rather than self-registering, so an operator controls which applications may request access and with what redirect URIs.

## What it cannot do (today)

- **No writes.** Sending a command or acknowledging an alarm through MCP is planned, but only behind an elevated scope *and* an explicit human confirmation — an assistant will never silently actuate a device.
- **No cross-tenant access.** The token is scoped to one tenant, chosen by the user at grant time.
- **No arbitrary queries.** Only the curated tool set is reachable; there is no `run_graphql`.

It is also **opt-in** — the `mcp` service is not part of a default deployment and is enabled explicitly by an operator.

## Related

- **[Multi-Tenancy](./multi-tenancy.md)** — how tenant isolation is enforced, which is what the MCP token rides on.
- **[Architecture](./architecture.md)** — where the `mcp` service sits, and the [secret handling](./architecture.md#secret-handling) model for credentials.
- **[GraphQL API](../reference/graphql-api.md)** — the API the MCP tools front.
