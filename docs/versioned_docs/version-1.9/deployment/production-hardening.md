{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Production hardening {#production-hardening}

Before opening an instance to the internet, check what its public port serves, use trusted TLS
certificates, and restrict cluster access. This page describes the service listeners of the
current release. Before v1.6.0, metrics and health probes were served on the public API port and
reachable through `/api/<area>/...`; upgrade before relying on the boundary described below.

## Public port and operations port {#public-port}

Each area's pods and Service have two HTTP ports:

| Port | Name | What it serves | Routed from outside |
| --- | --- | --- | --- |
| **8080** | `graphql` | `/graphql`, the area's own HTTP routes, and `/graphiql` when developer tools are enabled | Yes: the chart forwards `/api/<area>/<path>` to `/<path>` |
| **8083** | `ops` | `/metrics`, `/healthz` and `/readyz` | No: no chart route targets it |

The operational endpoints answer without authentication, which is why they live on a port no
public route reaches. Through the public prefix route, `/api/<area>/metrics`,
`/api/<area>/healthz` and `/api/<area>/readyz` return **404**: the API port does not serve those
paths at all. Kubernetes probes and the chart's Prometheus ServiceMonitors use the `ops` port
directly inside the cluster. See [Observability](./observability.md).

`/graphiql` serves the explorer page without authentication when developer tools are enabled.
Developer tools are disabled by default, and schema introspection is disabled with them.

`/graphql` is also reachable before sign-in: authentication and authorization depend on the
operation. Login, SSO initiation and public discovery must work before a user has a token;
protected operations still require verified credentials and permissions.

The service-token mint `/auth/service-token` and signing-key set `/auth/jwks` are on
user-management's separate internal listener, not port 8080. The chart publishes no route to
that listener; the public port returns 404 for those paths. Keep its Service internal.
Go profiles are off by default and use a separate loopback listener when enabled;
see [Profiling a service](./observability.md#profiling).

## Your own ingress or edge {#edge-rule}

When you supply your own ingress controller, Gateway or reverse proxy, route the API only to
the `graphql` port, never to `ops`, and never to user-management's internal listener. Close
direct public access to node ports, load balancers and service ports other than your edge, so
callers cannot reach the `ops` port by going around it.

If you enable developer tools on an instance reachable from the internet, block the explorer at
the edge as well. For an NGINX reverse proxy **in front of** the chart's ingress or Gateway, add
this location to the instance's HTTPS `server` block, before the general proxy location.
`devicechain_edge` stands for your existing upstream to the ingress or Gateway:

```nginx
location ~* ^/api/[^/]+/graphiql(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

Blocking the explorer does not block GraphQL queries, and with developer tools enabled schema
introspection stays available on `/graphql`. Keep developer tools disabled on an
internet-facing instance.

After an install, an upgrade or an edge change, check from outside without credentials:

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

`/metrics`, `/healthz` and `/readyz` must return **404**, or **403** if your own edge also
blocks them. `/graphiql` must return 404 with developer tools disabled, or 403 with the rule
above. Repeat for every enabled public area. Confirm sign-in and an authorized GraphQL request
still work, probes stay healthy, and Prometheus still scrapes the services. Treat any metrics or
health response from outside as an edge that routes to the `ops` port.

## Cluster and credential controls {#controls}

- Use trusted TLS certificates at the public edge and preserve broker TLS. See
  [Connecting from outside](./connecting-from-outside.md).
- Configure [trusted proxies](./bootstrap.md#trusted-proxies) for the actual proxy chain so
  client-address limits use the intended source address.
- Restrict network access to databases, the broker, monitoring and internal service ports,
  including `ops`; a public edge does not isolate pods from other cluster workloads.
- Limit Kubernetes permissions to read Secrets, exec into pods or port-forward. Keep profiling
  disabled outside a controlled diagnostic session.
- Protect the instance root key and backup material, and rehearse
  [disaster recovery](./disaster-recovery.md). Use least-privilege tenant roles and rotate leaked
  credentials. Review the remaining boundaries in the [Security model](../concepts/security-model.md).
