{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Production hardening {#production-hardening}

Before opening an instance to the internet, restrict its operational endpoints at the edge,
use trusted TLS certificates, and restrict cluster access. This page describes the current
service listeners; older releases can expose a different surface. Upgrade before relying on
the internal-listener boundary described below.

## Public port and authentication {#public-port}

The service HTTP port is **8080**. The chart forwards `/api/<area>/<path>` to `/<path>`
on each area with a public API. That prefix route also forwards operational endpoints;
it does not add authentication. These paths are reachable without a DeviceChain token:

| Service path | External path | What it serves |
| --- | --- | --- |
| `/metrics` | `/api/<area>/metrics` | Prometheus metrics, including operational labels; restrict to monitoring clients. |
| `/healthz` | `/api/<area>/healthz` | Liveness status; failure details stay in service logs. |
| `/readyz` | `/api/<area>/readyz` | Readiness status. |
| `/graphiql` | `/api/<area>/graphiql` | Explorer HTML only when developer tools are enabled; disabled by default. |

`/graphql` is also reachable before sign-in: authentication and authorization depend on the
operation. Login, SSO initiation and public discovery must work before a user has a token;
protected operations still require verified credentials and permissions. Blocking the explorer
does not block GraphQL queries. Schema introspection is disabled with developer tools by default.

The service-token mint `/auth/service-token` and signing-key set `/auth/jwks` are on
user-management's separate internal listener, not port 8080. The chart publishes no route to
that listener; the public port returns 404 for those paths. Keep its Service internal.
Go profiles are off by default and use a separate loopback listener when enabled;
see [Profiling a service](./observability.md#profiling).

## Block operational paths at the edge {#edge-rule}

For an NGINX reverse proxy **in front of** the chart's ingress or Gateway, add this location
to the instance's HTTPS `server` block, before the general proxy location. `devicechain_edge`
stands for your existing upstream to the ingress or Gateway. NGINX matches locations against
the normalized URI, without the query string. This rule blocks the paths and their descendants
for every area, before prefix rewriting:

```nginx
location ~* ^/api/[^/]+/(metrics|healthz|readyz|graphiql)(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

This is a reverse-proxy configuration, not an ingress annotation or a portable Gateway API
filter. On another edge, implement the same deny rule on normalized paths before forwarding.
Ensure no earlier regex location or `^~` location bypasses it. Close direct public access to
the upstream ingress/Gateway and service ports so callers cannot bypass the rule.
Keep Kubernetes probes and the Prometheus ServiceMonitor using their direct in-cluster paths;
they must not pass through this public deny rule. See [Observability](./observability.md).

After reloading the edge configuration, check from outside without credentials:

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

Each must return **403**, including with a query string or a trailing slash. Repeat for every
enabled public area. Confirm sign-in and an authorized GraphQL request still work, probes stay
healthy, and Prometheus still scrapes the services. Treat any operational response from outside
as a failed rollout of the edge rule.

## Cluster and credential controls {#controls}

- Use trusted TLS certificates at the public edge and preserve broker TLS. See
  [Connecting from outside](./connecting-from-outside.md).
- Configure [trusted proxies](./bootstrap.md#trusted-proxies) for the actual proxy chain so
  client-address limits use the intended source address.
- Restrict network access to databases, the broker, monitoring and internal service ports;
  a public edge rule does not isolate pods from other cluster workloads.
- Limit Kubernetes permissions to read Secrets, exec into pods or port-forward. Keep profiling
  disabled outside a controlled diagnostic session.
- Protect the instance root key and backup material, and rehearse
  [disaster recovery](./disaster-recovery.md). Use least-privilege tenant roles and rotate leaked
  credentials. Review the remaining boundaries in the [Security model](../concepts/security-model.md).
