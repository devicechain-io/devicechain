---
title: AI Access (MCP)
---

# AI Access (MCP)

You can let an AI assistant — Claude Desktop and Claude Code, Cursor, VS Code — work with a DeviceChain tenant on a user's behalf, through a **Model Context Protocol (MCP)** server. The LLM client connects, discovers a set of tools, and calls them to answer questions about your fleet: *"which devices in Building 3 haven't reported in the last hour?"*, *"summarize today's alarms for the cold-storage assets"*, *"what's the latest temperature on thermostat T-114?"*

The server is built on one principle: **an AI agent can never do more than the person who authorized it.** It is not a broad, over-permissioned gateway. It is a thin, curated, read-only layer over the platform's existing GraphQL API, and it carries the signed-in user's own tenant-scoped token.

:::note Status
**Available today (read-only):** an opt-in `mcp` service with eleven curated read tools, fronted by a full OAuth 2.1 authorization server on `user-management`. **Planned:** write tools (send command, acknowledge/clear alarm) behind an elevated scope and a mandatory human confirmation, and dynamic client registration (RFC 7591). This repository is the source of truth for what currently builds.
:::

The authorization server supports the authorization-code flow with PKCE, RFC 8414 metadata, refresh-token rotation and RFC 8707 audience binding. Until dynamic client registration ships, an administrator registers clients.

## What an assistant can do

The server exposes eleven **read** tools. Each one is a query against the same GraphQL API the console uses, run under the caller's token. A tool therefore returns exactly what that user, in that tenant, is allowed to see, and nothing more.

**Devices**

- `list_devices` — list devices, with filtering.
- `get_device` — a single device's details.
- `get_device_capabilities` — what a device can measure, and the published commands it accepts, with each command's parameter schema.

**Live state & telemetry**

- `get_device_state` — the device's current last-known state, including whether that state was *reported by the transport* or *inferred from silence*. The difference changes what "not active" means. Reported means the device is known to be disconnected. Inferred means only that nothing has arrived recently, which is also what a healthy device on a slow reporting interval looks like.
- `get_latest_measurements` — the most recent value per measurement.
- `query_measurements` — raw time-series readings over a time range.
- `aggregate_measurements` — bucketed aggregates (min/max/avg and the like) over a range.

**Position**

- `query_locations` — a device's reported positions over an optional time window, paged and bounded. Results are newest first, so the first one is the device's last known position; asking for a single result with no window answers "where is it now". Each position carries latitude and longitude. When the receiver reported them, it also carries elevation, accuracy, speed and heading. An absent field was not reported, and means unknown rather than zero. Reading positions needs two things the other tools do not. The client must also be authorized for the separate `location` scope; a client that wants both asks for `read-only location`. The user must also hold the **location** permission, which is deliberately not part of the read-only viewer baseline. This one tool can therefore be refused for a caller whose other read tools all work.

**Alarms**

- `list_alarms` — alarms, with filtering by state and entity.
- `get_alarm` — a single alarm's details.

**Commands**

- `list_commands` — the commands issued to a device and their status.

There is no generic "run this GraphQL query" tool. Sensitive reads — credentials, the audit trail, notification recipients, provisioning secrets — are deliberately left out of the tool set.

## The security model

MCP is becoming a standard way to give AI assistants real capabilities. The risk is that a careless implementation hands an agent a powerful, broadly scoped key. DeviceChain's server is designed so that this cannot happen.

- **It carries the user's token, never a service token.** The MCP server holds no privileged platform credential. Every tool call forwards the *caller's* validated, tenant-scoped JWT to the underlying GraphQL service, so the agent reaches exactly what the user reaches. Giving an AI a service identity of its own would let it act across tenants on anyone's request, and that is the one thing this design refuses.
- **The tenant is pinned at grant time, not passed as a parameter.** Authorization decides which tenant the token can act in, and the token carries that tenant. No tool takes a "tenant" argument an agent could change.
- **Tokens are audience-bound.** An access token issued for the MCP server names that server as its intended audience (RFC 8707) and is rejected anywhere else. A token minted for one resource can't be replayed against another.
- **Read-only, and curated.** Every tool is a query. There is no write path, no generic query escape hatch, and no exposure of sensitive objects.
- **Every call is authenticated and re-checked.** On each request the server validates the bearer token against `user-management`'s public keys and enforces a read-only scope. The underlying GraphQL service then independently re-applies the same tenant and role checks the console gets.

Connect an assistant, and it can *read devices, state, measurements, and alarms* for your tenant. It cannot reach another tenant, change anything, or run an arbitrary query.

## How a client connects

The MCP server is an **OAuth 2.1 resource server**, and `user-management` is its **authorization server**. Connecting a client is a standard OAuth flow, not a bespoke key exchange:

1. The client reads the server's requirements from its protected-resource metadata (RFC 9728), then finds the authorization server from *that* server's metadata (RFC 8414). Each document lives at a well-known path built by inserting the well-known segment **between** the host and the identifier's path. On an instance at `iot.example.com` they are `/.well-known/oauth-protected-resource/api/mcp` and `/.well-known/oauth-authorization-server/api/user-management`.
2. The client sends the user through the authorization-code flow with PKCE (`/oauth/authorize`). The user signs in, chooses the tenant to grant, and consents. All of it is server-rendered, with no shared secret.
3. The client exchanges the code for a tenant-scoped access token at `/oauth/token`, and refreshes it as needed. Refresh tokens are single-use and rotated. Resetting the user's password, disabling the user or deleting the user ends the grant: the next refresh is refused and the client must authorize again.
4. The client calls MCP tools with that token, and each call runs under the user's own permissions.

An administrator registers clients through the admin API; clients do not register themselves. An operator therefore controls which applications may request access, and with which redirect URIs.

### Where to point a client {#where-to-point-a-client}

You configure one URL: the instance's public host plus `/api/mcp`.

```
https://<your-instance-host>/api/mcp
```

That one string serves three purposes, which is why it is the only one you need:

- the **endpoint** the client POSTs its MCP requests to;
- the **resource identifier** the client sends as the `resource` parameter when it asks for a token, and that the token carries as its audience;
- the **starting point for discovery**, from which everything else is derived.

You enter nothing else by hand. When the endpoint answers `401`, the client reads the metadata location from the response's `WWW-Authenticate` header and follows it. That document tells it where the authorization server is, and the client then asks the authorization server for its own metadata. Discovery is those three requests, and you can walk them by hand before pointing a client at anything:

```bash
# 1. The endpoint answers 401 and names its metadata document.
curl -i -X POST https://<your-instance-host>/api/mcp

# 2. That document names the authorization server.
curl https://<your-instance-host>/.well-known/oauth-protected-resource/api/mcp

# 3. The authorization server describes where to log in and get a token.
curl https://<your-instance-host>/.well-known/oauth-authorization-server/api/user-management
```

The well-known segment goes **between** the host and the rest of the path, not after it. That looks odd at first, but it is the location the standards define for an identifier that carries a path, so it is what a client builds on its own. For the second document, the more intuitive-looking `https://<host>/api/mcp/.well-known/oauth-protected-resource` serves the same thing, for clients that build the path that way.

All three requests are unauthenticated: discovery is public by design and returns no secrets. Request 3 only answers once the authorization server is switched on; see [below](#limits-and-boundaries) for why that is a separate step.

### One replica only {#run-exactly-one-replica}

The MCP server keeps each client's protocol session in memory, on the pod that created it. Sessions are not shared between pods, and there is no session affinity. With a second replica, roughly half of every client's requests reach a pod that has never heard of the session, and are refused. The failure is intermittent and its message does not mention scaling, so it looks like a client bug.

:::warning Run exactly one replica
Installing the `mcp` area with more than one replica is refused outright, rather than left to fail intermittently at runtime.
:::

## Limits and boundaries {#limits-and-boundaries}

Some limits are decisions and some are bounds of the current implementation. Each group below says which.

**Deliberate, and not scheduled to change:**

- **No writes.** Sending a command or acknowledging an alarm through MCP is planned, but only behind an elevated scope *and* an explicit human confirmation. An assistant will never silently actuate a device.
- **No cross-tenant access.** The token is scoped to one tenant, chosen by the user at grant time. Tenancy is never a tool parameter, so there is no argument an agent can vary to reach another tenant.
- **No arbitrary queries.** Only the curated tool set is reachable; there is no `run_graphql`.
- **No service credential in any code path it runs.** This is stronger than "it does not use a service token": no code path in the MCP server reaches for a credential of its own, so an agent has no borrowed authority to exploit. Every downstream read goes out under the caller's own token, and an agent with no permission to read something gets the same refusal a person would. (Its pod mounts the instance configuration like every other service's does. That is deployment plumbing, not something the server uses.)
- **Command payloads are not returned.** `list_commands` gives you a command's name, status and timings. What was sent to the device stays out of the agent's context.

**Bounds you will hit before anything else:**

- Results are paged at 25 by default and 100 at most, and a multi-device lookup takes at most 50 tokens per call. An agent surveying a large fleet pages through it.
- The server reads at most 8 MiB from any single downstream response. That bounds what it fetches from the platform's own APIs; it is not a limit it advertises to the agent.
- A session idles out after 30 minutes.

**Enabling the service is not enough to use it.** There are two independent switches. Turning on only the first is the common way to end up with a server that answers but cannot be reached:

1. The `mcp` functional area is not in a default deployment; an operator enables it explicitly.
2. The authorization server on `user-management` stays off until an issuer URL is configured. Until then, `mcp` starts and serves its metadata, but **no client can obtain a token**. It is a separate switch on purpose: setting the issuer changes a claim on every token the instance mints, not just the ones MCP uses.

## Related

- **[Multi-Tenancy](./multi-tenancy.md)** — how tenant isolation is enforced, which is what the MCP token relies on.
- **[Architecture](./architecture.md)** — where the `mcp` service sits, and the [secret handling](./architecture.md#secret-handling) model for credentials.
- **[GraphQL API](../reference/graphql-api.md)** — the API the MCP tools front.
