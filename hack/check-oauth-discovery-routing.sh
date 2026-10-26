#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assert that the OAuth discovery walk an MCP client performs is routed end to end by
# the rendered ingress.
#
# WHY THIS EXISTS
#
# Discovery is three requests, and the client derives every one of them from a string a
# service handed it. Nothing inside a service can tell whether the URL it published
# reaches anything — the ingress decides that, in another language in another directory —
# and the reference client aborts the whole walk on the FIRST error rather than trying a
# later candidate, so one unrouted hop is the end of discovery, not a degraded path.
#
#   1. POST <resourceUrl>                        -> mcp, 401 + WWW-Authenticate
#   2. GET  <origin>/.well-known/oauth-protected-resource/<resource path>   -> mcp
#   3. GET  <origin>/.well-known/oauth-authorization-server/<issuer path>   -> user-management
#
# Steps 2 and 3 do not start with /api/<area>, so the API ingress does not match them and
# the console's "/" does — and the console answers 200 with index.html, which a client
# reports as a decode failure rather than as a routing problem. Every mcp package test
# was green with all three broken.
#
# 🔴 THE SUFFIX CONSTANTS ARE READ OUT OF THE GO SOURCE, NOT SPELLED HERE. The chart
# routes each well-known suffix as a prefix and leaves the path insertion to the service
# that owns the identifier, so the only thing the two sides share is the suffix string.
# A copy of it in this file would keep passing after the Go one moved, which is the one
# way the split can rot.
#
# Everything else is derived from the RENDERED configuration — the identifier and the
# issuer as the services actually receive them — so moving either without moving its
# route fails here.
#
# Usage:
#   hack/check-oauth-discovery-routing.sh             # verify (CI)
#   hack/check-oauth-discovery-routing.sh --self-test # prove the check can fail
set -euo pipefail

cd "$(dirname "$0")/.."

# DC_CHART_DIR exists so the negative control can aim this guard at a chart that is KNOWN to be
# wrong (the pre-change one); it is never set in CI.
CHART="${DC_CHART_DIR:-deploy/helm/devicechain}"
WELLKNOWN_GO="backend/core/auth/wellknown.go"
SSO_GO="backend/services/user-management/identity/sso_callback_handler.go"
ISSUER="https://iot.example.com/api/user-management"
# The front end under test: "nginx" (Ingress objects) or "gateway" (Gateway + HTTPRoutes).
# The default run checks both; the self-test proves each one's check can fail.
FRONT_END="nginx"

# render [extra helm args...] — the full profile on an ingress, with the Authorization
# Server switched on. The AS is a separate switch from the mcp area, so a render without
# it would exercise the AS route conditional in one direction only and report green over
# a rule that never appeared.
render() {
  helm template "$CHART" \
    --set profile=full \
    --set ingress.enabled=true \
    --set "ingress.controller=$FRONT_END" \
    --set ingress.host=iot.example.com \
    --set ingress.tls.enabled=true \
    --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" \
    --set "functionalAreas.user-management.config.auth.issuerUrl=$ISSUER" \
    "$@"
}

# ---------------------------------------------------------------------------
# check <rendered-manifest> <wellknown.go>
# ---------------------------------------------------------------------------
# 🔴 A missing YAML parser is a HARD FAILURE, never a skip. A guard that cannot read its
# input must not report on it: "clean" and "never looked" have to be distinguishable.
check() {
  python3 - "$1" "$2" "$3" "${4:-nginx}" <<'PY'
import json
import re
import sys
from urllib.parse import urlsplit

try:
    import yaml
except ImportError:
    sys.exit("ERROR: PyYAML is not available, so this guard parsed nothing. That is a "
             "failure, not a pass.")

manifest, gosrc, ssosrc, FRONT = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
if FRONT not in ("nginx", "gateway"):
    sys.exit(f"ERROR: unknown front end {FRONT!r}")

# --- the suffix constants, from the Go source that defines them -------------------
# The pattern requires a const ASSIGNMENT, so a mention in a comment or a doc example
# cannot satisfy it — this repository has been bitten by a "verified in code" grep that
# matched a comment.
src = open(gosrc).read()
def const(name):
    m = re.search(r'^\s*%s\s*=\s*"([^"]+)"\s*$' % re.escape(name), src, re.MULTILINE)
    if not m:
        sys.exit(f"ERROR: {gosrc} declares no const {name}. The chart routes whatever "
                 f"that constant says, so this guard cannot check the route against "
                 f"anything and must not report success.")
    return m.group(1)

PR_SUFFIX = const("ProtectedResourceMetadataSuffix")
AS_SUFFIX = const("AuthorizationServerMetadataSuffix")
if PR_SUFFIX == AS_SUFFIX or PR_SUFFIX.startswith(AS_SUFFIX) or AS_SUFFIX.startswith(PR_SUFFIX):
    sys.exit(f"ERROR: the two well-known suffixes overlap ({PR_SUFFIX!r}, {AS_SUFFIX!r}). "
             f"Routed as prefixes, one would swallow the other and hand clients the "
             f"wrong service's document.")

with open(manifest) as fh:
    docs = [d for d in yaml.safe_load_all(fh) if d]
if not docs:
    sys.exit(f"ERROR: {manifest} rendered no objects; the guard examined nothing.")

ingresses = [d for d in docs if d.get("kind") == "Ingress"]
routes = [d for d in docs if d.get("kind") == "HTTPRoute"]
gateways = [d for d in docs if d.get("kind") == "Gateway"]
if FRONT == "nginx":
    if not ingresses:
        sys.exit("ERROR: the render produced no Ingress objects, so nothing about routing "
                 "was checked. The chart was rendered with ingress.enabled=true.")
    if routes or gateways:
        sys.exit("ERROR: the nginx render carries a Gateway or HTTPRoute; exactly one front "
                 "end is rendered, so this is not the shape under test.")
else:
    if not routes or not gateways:
        sys.exit("ERROR: the render produced no Gateway and HTTPRoute objects, so nothing "
                 "about routing was checked. The chart was rendered with "
                 "ingress.controller=gateway.")
    if ingresses:
        sys.exit("ERROR: the gateway render carries Ingress objects; exactly one front "
                 "end is rendered, so this is not the shape under test.")

# --- what the services publish ----------------------------------------------------
# The config ConfigMap is each service's own input, so it is the authority for every
# expectation below rather than a value restated here.
def area_config(area):
    for d in docs:
        if d.get("kind") == "ConfigMap" and area in (d.get("data") or {}):
            try:
                return json.loads(d["data"][area])
            except (ValueError, TypeError):
                return None
    return None

mcp_cfg = area_config("mcp") or {}
resource = mcp_cfg.get("resourceUrl")
if not resource:
    sys.exit("ERROR: no rendered mcp configuration carrying a resourceUrl. Either the "
             "area did not render or its config moved; either way the routing below "
             "would be checked against nothing.")
um_cfg = area_config("user-management") or {}
issuer = (um_cfg.get("auth") or {}).get("issuerUrl")
if not issuer:
    sys.exit("ERROR: the render carries no user-management auth.issuerUrl, so the "
             "authorization-server half of the walk is unexercised. Render with it set; "
             "a green run over an absent route proves nothing.")

resource_host = urlsplit(resource).hostname
resource_path = urlsplit(resource).path.rstrip("/")
if not resource_path:
    sys.exit(f"ERROR: resourceUrl {resource!r} carries no path, so this render is not "
             "the ingress-hosted shape this guard was written for.")
if urlsplit(issuer).hostname != resource_host:
    sys.exit(f"ERROR: the issuer host and the resource host differ ({issuer!r} vs "
             f"{resource!r}); this guard assumes the single-host instance the chart builds.")

problems = []

if FRONT == "nginx":
    def matches(path_value):
        """Every (ingress, path-entry) whose path equals path_value."""
        out = []
        for ing in ingresses:
            meta = ing.get("metadata") or {}
            name = meta.get("name", "<unnamed>")
            rewrite = (meta.get("annotations") or {}).get(
                "nginx.ingress.kubernetes.io/rewrite-target")
            tls_hosts = set()
            for t in (ing.get("spec") or {}).get("tls") or []:
                tls_hosts.update(t.get("hosts") or [])
            for rule in (ing.get("spec") or {}).get("rules") or []:
                for p in ((rule.get("http") or {}).get("paths") or []):
                    if p.get("path") == path_value:
                        out.append({
                            "ingress": name,
                            "rewrite": rewrite,
                            "host": rule.get("host"),
                            "tls_hosts": tls_hosts,
                            "type": p.get("pathType"),
                            "service": (((p.get("backend") or {}).get("service")) or {}).get("name"),
                            "port": ((((p.get("backend") or {}).get("service")) or {}).get("port") or {}).get("name"),
                        })
        return out

    def expect(path_value, service, path_type, rewrite, why):
        """Assert exactly the properties a request needs to survive the hop."""
        found = matches(path_value)
        if not found:
            problems.append(f"nothing routes {path_value!r}. {why}")
            return
        for m in found:
            where = f"ingress {m['ingress']} path {path_value!r}"
            if m["service"] != service:
                problems.append(f"{where} is backed by {m['service']!r}, not {service!r}.")
            # The port is a NAME, and a Service that renamed its port would leave this
            # rule pointing at a port that does not exist — a 503 at the edge, with the
            # rule still present and looking right.
            if m["port"] != "graphql":
                problems.append(f"{where} names backend port {m['port']!r}, not 'graphql'. "
                                f"The Service publishes 'graphql'; any other name resolves "
                                f"to nothing and the edge answers 503.")
            if m["type"] != path_type:
                problems.append(f"{where} has pathType {m['type']!r}, want {path_type!r}.")
            if m["rewrite"] != rewrite:
                problems.append(f"{where} has rewrite-target {m['rewrite']!r}, want {rewrite!r}.")
            # A rule on the wrong host is a rule for a request nobody makes, and it looks
            # entirely correct in the rendered YAML.
            if m["host"] != resource_host:
                problems.append(f"{where} is attached to host {m['host']!r}, but clients "
                                f"reach this instance at {resource_host!r} (from the "
                                f"rendered identifier {resource!r}).")
            # TLS terminates per host per ingress. A rule on an object whose TLS block omits
            # the host is served plaintext-only; an https client gets a handshake failure,
            # which reads as a certificate problem rather than a chart one.
            if resource_host not in m["tls_hosts"]:
                problems.append(f"{where} sits on an object whose TLS hosts are "
                                f"{sorted(m['tls_hosts'])!r}, which does not include "
                                f"{resource_host!r}. Every URL in the walk is https.")

    # --- 1. the endpoint --------------------------------------------------------------
    # The identifier itself must reach the MCP endpoint. The API ingress matches
    # /api/<area>(/|$)(.*) and rewrites to /$2, so the bare identifier path arrives at the
    # pod as "/" — which is where the handler is mounted (pinned by
    # TestDiscoveryWalkReachesTheMetadataDocument in backend/services/mcp/server).
    expect(f"{resource_path}(/|$)(.*)", "mcp", "ImplementationSpecific", "/$2",
           f"A client POSTs {resource!r} verbatim; unrouted, it reaches the console.")

    # --- 2. the protected-resource metadata -------------------------------------------
    # The SUFFIX is routed as a prefix, unrewritten: the service owns the whole namespace
    # and decides which exact path under it is its document, so the chart never computes
    # the RFC 9728 insertion and cannot disagree with the Go code about it.
    expect(PR_SUFFIX, "mcp", "Prefix", None,
           f"That is the namespace a client's metadata URL falls in (the suffix inserted "
           f"before {resource_path!r}), and it is what the 401 challenge advertises. "
           f"Unrouted, it falls through to the console and the client is handed index.html.")

    # --- 3. the authorization-server metadata -----------------------------------------
    expect(AS_SUFFIX, "user-management", "Prefix", None,
           f"That is where a client looks for the metadata of {issuer!r}, named in the "
           f"authorization_servers field of the document fetched at step 2. The reference "
           f"client aborts on the first error, so an unrouted step 3 ends the walk even "
           f"though later fallback locations exist.")

    # --- 4. the single sign-on callback -----------------------------------------------
    # A provider redirects the browser to <issuer>/sso/callback/<connection>, an address the
    # service builds from its own issuer and the Go route prefix below. Nothing in the service
    # can tell whether the edge delivers it: it must match the API route for user-management,
    # survive that route's rewrite as the pod-local path the handler is registered at, and not
    # be refused by anything at the edge.
    #
    # 🔴 THE PREFIX IS READ OUT OF THE GO SOURCE, not spelled here, for the reason the
    # well-known suffixes are: the only thing the two sides share is that string.
    m = re.search(r'^const\s+SsoCallbackPrefix\s*=\s*"([^"]+)"\s*$', open(ssosrc).read(), re.MULTILINE)
    if not m:
        sys.exit(f"ERROR: {ssosrc} declares no const SsoCallbackPrefix. The service registers "
                 f"whatever that says, so the callback route cannot be checked against anything.")
    SSO_PREFIX = m.group(1)
    issuer_path = urlsplit(issuer).path.rstrip("/")
    callback = f"{issuer_path}{SSO_PREFIX}acme-idp"

    routed = []
    for ing in ingresses:
        meta = ing.get("metadata") or {}
        ann = meta.get("annotations") or {}
        for rule in (ing.get("spec") or {}).get("rules") or []:
            for p in ((rule.get("http") or {}).get("paths") or []):
                pat = str(p.get("path", ""))
                if p.get("pathType") != "ImplementationSpecific" or not pat.startswith("/api/"):
                    continue
                mm = re.match(pat, callback)
                if mm:
                    routed.append((meta.get("name", "<unnamed>"), pat, mm, ann,
                                   (((p.get("backend") or {}).get("service")) or {}).get("name")))
    if not routed:
        problems.append(f"no API route matches the single sign-on callback {callback!r}, so a "
                        f"provider's redirect would reach the console and fail there.")
    for name, pat, mm, ann, svc in routed:
        where = f"ingress {name} path {pat!r}"
        if svc != "user-management":
            problems.append(f"{where} carries the single sign-on callback {callback!r} to "
                            f"{svc!r}, not user-management.")
        rewrite = ann.get("nginx.ingress.kubernetes.io/rewrite-target")
        if rewrite != "/$2":
            problems.append(f"{where} has rewrite-target {rewrite!r}; the callback needs '/$2' to "
                            f"arrive at the pod as {SSO_PREFIX}acme-idp.")
        else:
            arrives = "/" + mm.group(2)
            if arrives != f"{SSO_PREFIX}acme-idp":
                problems.append(f"{where} delivers the callback as {arrives!r}, not "
                                f"{SSO_PREFIX + 'acme-idp'!r}, the path user-management registers.")

    # --- 5. nothing at the edge is a security control --------------------------------------
    # user-management serves the service-token mint and the JWKS on an INTERNAL listener that
    # no route reaches, so the public port answers 404 for them with no ingress in front. That
    # is why the chart carries no configuration-snippet (the shared controller is tightened
    # in a later release, once no instance still needs one). Three things must stay true of the RENDER for that to hold:
    #   (a) no Ingress carries a snippet annotation of any kind (a snippet is how an edge rule
    #       would creep back in, and it is the attack class behind IngressNightmare);
    #   (b) no Ingress backend names the internal port, by name or number;
    #   (c) the instance config tells every peer to dial the port the user-management Service
    #       publishes as `internal-auth`, and that port is not the public one.
    for ing in ingresses:
        name = (ing.get("metadata") or {}).get("name", "<unnamed>")
        for key in ((ing.get("metadata") or {}).get("annotations") or {}):
            if "snippet" in str(key).lower():
                problems.append(f"ingress {name} carries the snippet annotation {key!r}. The chart "
                                f"must not depend on edge configuration for any security property, "
                                f"and the controller is to be run with snippets disabled.")


else:
    # ----------------------------------------------------------------------------------
    # Gateway API. The same walk, resolved the way a Gateway API controller resolves a
    # request, not by looking for a literal path string: among every rule of every HTTPRoute
    # that admits the host, the matching PathPrefix (whole path elements) with the LONGEST
    # value wins, an Exact match beats a prefix, and URLRewrite ReplacePrefixMatch rewrites
    # the matched prefix. There is no regex and no rule order to model, which is the point
    # of the move; what has to be proved instead is that the winner of each request in the
    # walk is the route that should have won it.
    # ----------------------------------------------------------------------------------
    tls_on = urlsplit(resource).scheme == "https"

    def elem_prefix(prefix, path):
        pp = [e for e in prefix.split("/") if e]
        rp = [e for e in path.split("/") if e]
        return rp[: len(pp)] == pp

    def listener_of(route):
        refs = (route.get("spec") or {}).get("parentRefs") or []
        return [r.get("sectionName") for r in refs if r.get("name") == "devicechain"]

    def resolve(path):
        """The route, backend and upstream path a request on the app listener reaches, or
        None. The app listener is https when the origin is."""
        listener = "https" if tls_on else "http"
        best = None
        for r in routes:
            spec = r.get("spec") or {}
            hosts = spec.get("hostnames") or []
            if hosts and resource_host not in hosts:
                continue
            if listener not in listener_of(r):
                continue
            for rule in spec.get("rules") or []:
                for m in rule.get("matches") or [{}]:
                    pm = m.get("path") or {"type": "PathPrefix", "value": "/"}
                    v, t = pm.get("value", "/"), pm.get("type", "PathPrefix")
                    if t == "Exact":
                        hit, rank = path == v, (2, len(v))
                    elif t == "PathPrefix":
                        hit, rank = elem_prefix(v, path), (1, len(v.rstrip("/")))
                    else:
                        continue
                    if hit and (best is None or rank > best[0]):
                        best = (rank, r, rule, v, t)
        if best is None:
            return None
        _, r, rule, v, t = best
        upstream = path
        for f in rule.get("filters") or []:
            if f.get("type") == "URLRewrite":
                rp = ((f.get("urlRewrite") or {}).get("path") or {})
                if rp.get("type") == "ReplacePrefixMatch":
                    rest = path[len(v.rstrip("/")):]
                    base = rp.get("replacePrefixMatch", "/").rstrip("/")
                    upstream = (base + rest) or "/"
        refs = rule.get("backendRefs") or []
        return {
            "route": (r.get("metadata") or {}).get("name", "<unnamed>"),
            "service": refs[0].get("name") if refs else None,
            "port": refs[0].get("port") if refs else None,
            "prefix": v, "type": t, "upstream": upstream,
            "redirect": any(f.get("type") == "RequestRedirect" for f in rule.get("filters") or []),
        }

    services_by_name = {(d.get("metadata") or {}).get("name"): d for d in docs if d.get("kind") == "Service"}

    def service_port(name):
        for sp in ((services_by_name.get(name) or {}).get("spec") or {}).get("ports") or []:
            if sp.get("name") == "graphql":
                return sp.get("port")
        return None

    def expect_gw(path, service, upstream, why):
        got = resolve(path)
        if got is None:
            problems.append(f"nothing routes {path!r}. {why}")
            return
        if got["redirect"]:
            problems.append(f"{path!r} reaches a redirect route ({got['route']}), not {service!r}.")
        if got["service"] != service:
            problems.append(f"{path!r} is served by route {got['route']} (prefix {got['prefix']!r}), "
                            f"backed by {got['service']!r}, not {service!r}. {why}")
        # A backendRef names the port by NUMBER. A Service that moved its port would leave this
        # rule pointing at one that does not exist: a 503 at the edge, rule still in place.
        want_port = service_port(service)
        if want_port is not None and got["port"] != want_port:
            problems.append(f"{path!r} routes to {service!r} on port {got['port']!r}, but the "
                            f"Service publishes 'graphql' on {want_port!r}; any other number "
                            f"resolves to nothing and the edge answers 503.")
        if got["upstream"] != upstream:
            problems.append(f"{path!r} reaches the pod as {got['upstream']!r} (route {got['route']}, "
                            f"prefix {got['prefix']!r}), want {upstream!r}.")

    # The listener the walk arrives on must exist, carry the host, and (with TLS) terminate it.
    gw = gateways[0]
    listeners = {l.get("name"): l for l in (gw.get("spec") or {}).get("listeners") or []}
    app_listener = listeners.get("https" if tls_on else "http")
    if app_listener is None:
        problems.append(f"the Gateway has no {'https' if tls_on else 'http'} listener, so every URL "
                        f"in the walk reaches nothing.")
    else:
        if app_listener.get("hostname") != resource_host:
            problems.append(f"the Gateway's app listener is for host {app_listener.get('hostname')!r}, but "
                            f"clients reach this instance at {resource_host!r}.")
        if tls_on and not (((app_listener.get("tls") or {}).get("certificateRefs")) or []):
            problems.append("the Gateway's https listener names no certificate, so it cannot terminate "
                            "TLS; every URL in the walk is https.")
    if tls_on:
        # Plain HTTP must redirect, never serve: a route that answers on the http listener would
        # carry the walk (and a bearer token) in the clear.
        for r in routes:
            if "http" in listener_of(r):
                for rule in (r.get("spec") or {}).get("rules") or []:
                    if not any(f.get("type") == "RequestRedirect" for f in rule.get("filters") or []):
                        problems.append(f"route {(r.get('metadata') or {}).get('name')} serves the http "
                                        f"listener without redirecting, on an instance that terminates TLS.")

    # 1. The endpoint: the identifier itself, and below it, reach mcp at its own root.
    expect_gw(resource_path, "mcp", "/",
              f"A client POSTs {resource!r} verbatim; unrouted, it reaches the console.")
    expect_gw(resource_path + "/", "mcp", "/",
              "The identifier with a trailing slash is the same endpoint.")
    # 2. The protected-resource metadata, as a prefix, unrewritten, in both spellings a client builds.
    expect_gw(PR_SUFFIX + resource_path, "mcp", PR_SUFFIX + resource_path,
              f"That is the metadata URL the 401 challenge advertises (the suffix inserted before "
              f"{resource_path!r}). Unrouted, it falls through to the console and the client is handed index.html.")
    expect_gw(resource_path + PR_SUFFIX, "mcp", "/" + PR_SUFFIX.lstrip("/"),
              "The appended form, for clients that build it: under the mcp prefix, so rewritten to mcp's own root.")
    # 3. The authorization-server metadata.
    issuer_path = urlsplit(issuer).path.rstrip("/")
    expect_gw(AS_SUFFIX + issuer_path, "user-management", AS_SUFFIX + issuer_path,
              f"That is where a client looks for the metadata of {issuer!r}. The reference client aborts on "
              f"the first error, so an unrouted step 3 ends the walk.")
    # 4. The single sign-on callback arrives at the pod as the path the handler is registered at.
    m = re.search(r'^const\s+SsoCallbackPrefix\s*=\s*"([^"]+)"\s*$', open(ssosrc).read(), re.MULTILINE)
    if not m:
        sys.exit(f"ERROR: {ssosrc} declares no const SsoCallbackPrefix. The service registers "
                 f"whatever that says, so the callback route cannot be checked against anything.")
    SSO_PREFIX = m.group(1)
    callback = f"{issuer_path}{SSO_PREFIX}acme-idp"
    expect_gw(callback, "user-management", f"{SSO_PREFIX}acme-idp",
              "A provider redirects the browser here; it must arrive at the pod as the path the handler is registered at.")
    # The console owns "/": the walk's neighbours must not have been taken from it.
    got = resolve("/")
    if got is None or got["service"] != "frontend":
        problems.append(f"'/' is served by {None if got is None else got['service']!r}, not the console (frontend).")

    # 5a. Nothing at the edge is a security control: no object carries a snippet-shaped annotation.
    for d in routes + gateways:
        for key in ((d.get("metadata") or {}).get("annotations") or {}):
            if "snippet" in str(key).lower():
                problems.append(f"{d.get('kind')} {(d.get('metadata') or {}).get('name')} carries the "
                                f"snippet-shaped annotation {key!r}; the chart must not depend on edge "
                                f"configuration for any security property.")

services = {(d.get("metadata") or {}).get("name"): d for d in docs if d.get("kind") == "Service"}
um_svc = services.get("user-management")
internal_port = None
public_port = None
if um_svc is None:
    problems.append("no user-management Service rendered, so the internal listener's port "
                    "could not be checked.")
else:
    for sp in (um_svc.get("spec") or {}).get("ports") or []:
        if sp.get("name") == "internal-auth":
            internal_port = sp.get("port")
        if sp.get("name") == "graphql":
            public_port = sp.get("port")
    if internal_port is None:
        problems.append("the user-management Service publishes no 'internal-auth' port, so "
                        "peers have nowhere to mint a service token or fetch the JWKS.")
    elif internal_port == public_port:
        problems.append(f"the internal-auth port equals the public port ({public_port}); the "
                        f"internal endpoints would be served where the ingress routes.")

# The pod must actually listen where the Service sends traffic: a Service port whose targetPort
# names a container port the Deployment does not declare is a connection refused on every mint.
um_dep = next((d for d in docs if d.get("kind") == "Deployment"
               and (d.get("metadata") or {}).get("name") == "user-management"), None)
if um_dep is None:
    problems.append("no user-management Deployment rendered, so its internal-auth container port "
                    "could not be checked.")
elif internal_port is not None:
    declared = {}
    for c in (((um_dep.get("spec") or {}).get("template") or {}).get("spec") or {}).get("containers") or []:
        for cp in c.get("ports") or []:
            declared[cp.get("name")] = cp.get("containerPort")
    if declared.get("internal-auth") != internal_port:
        problems.append(f"the user-management Deployment declares container port "
                        f"{declared.get('internal-auth')!r} for internal-auth, but its Service "
                        f"publishes {internal_port!r}.")

def is_internal(port):
    return port.get("name") == "internal-auth" or (
        internal_port is not None and port.get("number") == internal_port)

if FRONT == "nginx":
    for ing in ingresses:
        name = (ing.get("metadata") or {}).get("name", "<unnamed>")
        for rule in (ing.get("spec") or {}).get("rules") or []:
            for p in ((rule.get("http") or {}).get("paths") or []):
                port = ((((p.get("backend") or {}).get("service")) or {}).get("port") or {})
                if is_internal(port):
                    problems.append(f"ingress {name} path {p.get('path')!r} routes to the internal-auth "
                                    f"port. That listener serves the service-token mint and must "
                                    f"never be reachable from an ingress.")
        default_port = ((((((ing.get("spec") or {}).get("defaultBackend") or {}).get("service")) or {})
                         .get("port")) or {})
        if is_internal(default_port):
            problems.append(f"ingress {name} defaultBackend routes to the internal-auth port.")

else:
    # Gateway: no route may carry the internal-auth listener, by number (a backendRef has no names).
    for r in routes:
        for rule in (r.get("spec") or {}).get("rules") or []:
            for ref in rule.get("backendRefs") or []:
                if ref.get("name") == "user-management" and internal_port is not None and ref.get("port") == internal_port:
                    problems.append(f"route {(r.get('metadata') or {}).get('name')} routes to the internal-auth "
                                    f"port {internal_port}. That listener serves the service-token mint and "
                                    f"must never be reachable from the edge.")
instance_cfg = None
for d in docs:
    if d.get("kind") == "Secret" and "instance" in (d.get("stringData") or {}):
        try:
            instance_cfg = json.loads(d["stringData"]["instance"])
        except (ValueError, TypeError):
            pass
if instance_cfg is None:
    problems.append("no rendered instance config Secret, so the callers' internal port could not be checked.")
else:
    um_infra = ((instance_cfg.get("infrastructure") or {}).get("userManagement") or {})
    if internal_port is not None and um_infra.get("internalPort") != internal_port:
        problems.append(f"the instance config tells peers to dial user-management on internalPort "
                        f"{um_infra.get('internalPort')!r}, but its Service publishes internal-auth "
                        f"on {internal_port!r}.")
    if um_infra.get("internalPort") == um_infra.get("port"):
        problems.append("the instance config gives user-management one port for both the public and "
                        "internal listeners.")

if problems:
    print("OAuth discovery routing check FAILED:", file=sys.stderr)
    for p in problems:
        print(f"  - {p}", file=sys.stderr)
    sys.exit(1)

if FRONT == "nginx":
    print(f"OAuth discovery routing OK on host {resource_host} (Ingress objects)")
    print(f"  1. endpoint   {resource_path}(/|$)(.*) -> mcp, rewritten to '/'")
    print(f"  2. resource   {PR_SUFFIX} (prefix) -> mcp, unrewritten")
    print(f"  3. auth srv   {AS_SUFFIX} (prefix) -> user-management, unrewritten")
    print(f"  4. sso        {callback} -> user-management, rewritten to {SSO_PREFIX}acme-idp, not refused")
    print(f"  5. no ingress carries a snippet, none routes to the internal-auth port ({internal_port}), "
          f"and the instance config dials it")
    print(f"  also served   {resource_path}{PR_SUFFIX} -> mcp, rewritten to '{PR_SUFFIX}' "
          f"(the appended form, for clients that build it)")
else:
    print(f"OAuth discovery routing OK on host {resource_host} (Gateway API: longest PathPrefix wins)")
    print(f"  1. endpoint   {resource_path} and {resource_path}/ -> mcp, rewritten to '/'")
    print(f"  2. resource   {PR_SUFFIX}{resource_path} -> mcp, unrewritten")
    print(f"  3. auth srv   {AS_SUFFIX}{issuer_path} -> user-management, unrewritten")
    print(f"  4. sso        {callback} -> user-management, arrives as {SSO_PREFIX}acme-idp")
    print(f"  5. the app listener terminates TLS for the host, plain http only redirects, no object carries a "
          f"snippet, none routes to the internal-auth port ({internal_port}), and the instance config dials it")
    print(f"  also served   {resource_path}{PR_SUFFIX} -> mcp, rewritten to '/{PR_SUFFIX.lstrip('/')}'")
PY
}

# ---------------------------------------------------------------------------
# render_guards
# ---------------------------------------------------------------------------
# The mcp guards whose whole behaviour is whether a render SUCCEEDS or FAILS, and which
# nothing else exercises.
#
# 🔴 A REFUSAL IS ASSERTED ON ITS MESSAGE, NOT ON ITS EXIT STATUS. `helm template` exits
# non-zero for a typo, a missing value, an unrelated guard firing — so "it failed" is
# satisfied by any of those, and a guard that stopped firing entirely would still look
# green the moment anything else went wrong.
render_guards() {
  local out

  refuses() { # refuses <expected-substring> <description> [helm args...]
    local want="$1" what="$2"; shift 2
    if out="$(render "$@" 2>&1)"; then
      echo "FAILED: $what rendered successfully; it must be refused." >&2
      exit 1
    fi
    if ! grep -qF "$want" <<<"$out"; then
      echo "FAILED: $what was refused, but not for the expected reason." >&2
      echo "Expected the message to contain: $want" >&2
      echo "Got: $out" >&2
      exit 1
    fi
  }

  # 1. mcp cannot be scaled out: its protocol sessions live in the pod that made them.
  refuses "can only run ONE serving pod" "functionalAreas.mcp.replicas=2" \
    --set functionalAreas.mcp.replicas=2

  # 2. An identifier the ingress does not route is refused, rather than rendered and
  # left to fail as a client problem.
  refuses "only routes" "an off-host resourceUrl override" \
    --set 'functionalAreas.mcp.config.resourceUrl=https://mcp.example.com/mcp'

  # 3. A trailing slash is a second spelling of one identifier, and every comparison
  # downstream is exact.
  refuses "trailing slash" "a trailing-slash resourceUrl" \
    --set 'functionalAreas.mcp.config.resourceUrl=https://iot.example.com/api/mcp/'

  # 4. The loopback escape hatch is ACCEPTED, and it is the reason the override exists:
  # a port-forwarded mcp is reached at an address no ingress rule serves. This is the
  # counterweight to 2 and 3 — without it they are satisfied by a chart that refuses
  # every override.
  if ! out="$(render --set 'functionalAreas.mcp.config.resourceUrl=http://localhost:8080/api/mcp' 2>&1)"; then
    echo "FAILED: an explicit loopback resourceUrl was refused. It is the port-forward" >&2
    echo "shape, which no ingress rule serves and none needs to, and the service" >&2
    echo "accepts it (http is allowed for a loopback host):" >&2
    echo "$out" >&2
    exit 1
  fi
  if ! grep -q 'http://localhost:8080/api/mcp' <<<"$out"; then
    echo "FAILED: the loopback override did not reach the rendered config, so the" >&2
    echo "acceptance above proved nothing about it." >&2
    exit 1
  fi

  # 5. With no Authorization Server issuer there is no AS, so there must be no route to
  # one. Exercising the conditional in BOTH directions is what stops "the rule is always
  # there" from passing as "the rule appears when it should". An explicit EMPTY issuer is
  # how an operator keeps the AS (and single sign-on) off on an instance that would derive
  # one, so it must win over the derivation.
  out="$(render --set 'functionalAreas.user-management.config.auth.issuerUrl=' 2>&1)"
  if grep -q 'oauth-authorization-server' <<<"$out"; then
    echo "FAILED: an authorization-server metadata route rendered with no issuer" >&2
    echo "configured. There is no AS to route to, so the rule points at a 404." >&2
    exit 1
  fi

  echo "mcp render guards OK: replicas>1, off-host and trailing-slash identifiers refused"
  echo "  by name; the loopback override accepted; no AS route without an issuer"
}

# ---------------------------------------------------------------------------
# issuer_derivation_guards
# ---------------------------------------------------------------------------
# user-management's issuer is DERIVED from the ingress when the operator sets none, which is
# what makes single sign-on and the OAuth authorization server work on a default install.
# Derivation is only safe for an origin the service will accept (https, or a loopback host);
# for any other it must render NOTHING rather than a value that CrashLoops the service.
#
# These render the default profile (no mcp: mcp refuses an http origin on a real host, which
# is a different guard) and read what the service would receive from the config ConfigMap.
issuer_derivation_guards() {
  local key out
  key="$(openssl rand -base64 32)"

  issuer_of() { # issuer_of [helm args...] — prints the rendered auth.issuerUrl or nothing
    # Render into a variable first: the reader below stops at its first match, and a pipe
    # from helm would then SIGPIPE helm, which pipefail turns into exit 141.
    local rendered
    rendered="$(helm template "$CHART" \
        --set ingress.enabled=true --set ingress.host=iot.example.com \
        --set "instance.config.infrastructure.secrets.rootKey=$key" "$@")" || return 1
    python3 -c '
import json, sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "ConfigMap" and "user-management" in (d.get("data") or {}):
        print((json.loads(d["data"]["user-management"]).get("auth") or {}).get("issuerUrl", ""))
        break
else:
    sys.exit("no user-management configuration rendered")
' <<<"$rendered"
  }

  expect_issuer() { # expect_issuer <want> <description> [helm args...]
    local want="$1" what="$2" got; shift 2
    got="$(issuer_of "$@")"
    if [ "$got" != "$want" ]; then
      echo "FAILED: $what: user-management would receive issuerUrl '$got', want '$want'." >&2
      exit 1
    fi
  }

  expect_issuer "https://iot.example.com/api/user-management" \
      "an https ingress derives the issuer" --set ingress.tls.enabled=true
  # Derived from the same origin as mcp's, so the two agree byte for byte.
  render_no_issuer | python3 -c '
import json, sys, yaml
um = mcp = None
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "ConfigMap":
        data = d.get("data") or {}
        if "user-management" in data:
            um = (json.loads(data["user-management"]).get("auth") or {}).get("issuerUrl")
        if "mcp" in data:
            mcp = json.loads(data["mcp"]).get("issuerUrl")
if not um or um != mcp:
    sys.exit("FAILED: user-management derived %r but mcp points at %r; they must be the same string" % (um, mcp))
'
  # http on a real host would be refused by the service at startup, so nothing is derived.
  expect_issuer "" "an http ingress on a real host derives nothing" --set ingress.tls.enabled=false
  expect_issuer "http://iot.example.com/api/user-management" \
      "(control) the same http origin IS used when the operator sets it explicitly" \
      --set ingress.tls.enabled=false \
      --set 'functionalAreas.user-management.config.auth.issuerUrl=http://iot.example.com/api/user-management'
  expect_issuer "http://localhost/api/user-management" "a loopback http ingress derives" \
      --set ingress.host=localhost --set ingress.tls.enabled=false
  # An explicit value, empty included, wins over the derivation.
  expect_issuer "https://sso.example.com/custom" "an explicit issuer wins" \
      --set ingress.tls.enabled=true \
      --set 'functionalAreas.user-management.config.auth.issuerUrl=https://sso.example.com/custom'
  expect_issuer "" "an explicit empty issuer switches the derivation off" \
      --set ingress.tls.enabled=true --set 'functionalAreas.user-management.config.auth.issuerUrl='
  # No ingress, no origin, no issuer.
  got="$(helm template "$CHART" --set "instance.config.infrastructure.secrets.rootKey=$key" |
    python3 -c '
import json, sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "ConfigMap" and "user-management" in (d.get("data") or {}):
        print((json.loads(d["data"]["user-management"]).get("auth") or {}).get("issuerUrl", ""))
')"
  if [ -n "$got" ]; then
    echo "FAILED: an instance with no ingress was given the issuer '$got'." >&2
    exit 1
  fi
  echo "issuer derivation guards OK: https and loopback derive, other http does not, an"
  echo "  explicit value (empty included) wins, no ingress derives nothing"
}

# render_no_issuer — the full profile on a TLS ingress with NO issuer set, so mcp and
# user-management both derive theirs.
render_no_issuer() {
  helm template "$CHART" --set profile=full     --set ingress.enabled=true --set ingress.host=iot.example.com --set ingress.tls.enabled=true     --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)"
}

self_test_nginx() {
  FRONT_END=nginx
  # 🔴 THE NEGATIVE CONTROL. A check that has only ever been run against a correct render
  # has not been shown to fail. It corrupts the RENDERED manifest rather than the chart,
  # so each corruption is exactly one defect with nothing else disturbed — and there is
  # one per property the check asserts, because a check with five assertions and one
  # demonstrated failure has only been shown to be able to fail for one reason.
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp:-}"' RETURN
  render > "$tmp/full.yaml"

  if ! check "$tmp/full.yaml" "$WELLKNOWN_GO" "$SSO_GO" "$FRONT_END" >/dev/null; then
    echo "self-test: the unmodified render already fails, so a failure proves nothing." >&2
    exit 1
  fi

  corrupt() { # corrupt <name> <python-body-file>
    python3 - "$tmp/full.yaml" "$tmp/$1.yaml" "$2" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
ns = {"docs": docs, "sys": sys}
exec(open(sys.argv[3]).read(), ns)
if not ns.get("touched"):
    sys.exit("self-test: the corruption changed nothing, so the expected failure "
             "would be meaningless.")
yaml.safe_dump_all(docs, open(sys.argv[2], "w"))
PY
    if check "$tmp/$1.yaml" "$WELLKNOWN_GO" "$SSO_GO" "$FRONT_END" >/dev/null 2>&1; then
      echo "self-test FAILED: the check passed a render corrupted by '$1'." >&2
      exit 1
    fi
  }

  wk() { # wk <python-expression-body> — mutate every well-known path entry
    cat > "$tmp/op.py" <<PYOP
touched = False
for d in docs:
    if d.get("kind") != "Ingress":
        continue
    for rule in (d.get("spec") or {}).get("rules") or []:
        for p in ((rule.get("http") or {}).get("paths") or []):
            if str(p.get("path", "")).startswith("/.well-known/"):
                $1
                touched = True
PYOP
  }

  # Every property the check asserts, one corruption each.
  wk 'p["path"] = "/.well-known/nope"';              corrupt route-removed "$tmp/op.py"
  wk 'p["pathType"] = "Exact"';                      corrupt path-type "$tmp/op.py"
  wk 'p["backend"]["service"]["port"]["name"] = "grpc"'; corrupt port-name "$tmp/op.py"
  wk 'p["backend"]["service"]["name"] = "frontend"'; corrupt wrong-backend "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-wellknown":
        meta = d["metadata"]
        if not meta.get("annotations"):
            # Not setdefault: the template emits an `annotations:` key whose value is
            # null (the key, then only comments), so setdefault finds it and returns None.
            meta["annotations"] = {}
        meta["annotations"]["nginx.ingress.kubernetes.io/rewrite-target"] = "/$2"
        touched = True
PYOP
  corrupt rewritten "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-wellknown":
        for rule in (d.get("spec") or {}).get("rules") or []:
            rule["host"] = "elsewhere.example.com"
            touched = True
PYOP
  corrupt wrong-host "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-wellknown":
        if d["spec"].pop("tls", None) is not None:
            touched = True
PYOP
  corrupt tls-removed "$tmp/op.py"

  # The single sign-on callback: one corruption per way it can stop arriving.
  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        for rule in (d.get("spec") or {}).get("rules") or []:
            for p in ((rule.get("http") or {}).get("paths") or []):
                if str(p.get("path", "")).startswith("/api/user-management"):
                    p["backend"]["service"]["name"] = "frontend"
                    touched = True
PYOP
  corrupt sso-wrong-backend "$tmp/op.py"

  # The edge is not a security control: each way it could become one again, or the internal
  # listener could become reachable, is a corruption of its own.
  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        d["metadata"]["annotations"]["nginx.ingress.kubernetes.io/configuration-snippet"] = \
            'if ($request_uri ~* "^/api/[^/]+/auth(/|$)") { return 404; }'
        touched = True
PYOP
  corrupt snippet-annotation "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-web":
        if not d["metadata"].get("annotations"):
            d["metadata"]["annotations"] = {}
        d["metadata"]["annotations"]["nginx.ingress.kubernetes.io/server-snippet"] = "#"
        touched = True
PYOP
  corrupt server-snippet-on-another-ingress "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        for rule in (d.get("spec") or {}).get("rules") or []:
            for p in ((rule.get("http") or {}).get("paths") or []):
                if str(p.get("path", "")).startswith("/api/user-management"):
                    p["backend"]["service"]["port"] = {"name": "internal-auth"}
                    touched = True
PYOP
  corrupt route-to-internal-port-by-name "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-web":
        d["spec"]["defaultBackend"] = {"service": {"name": "user-management", "port": {"name": "internal-auth"}}}
        touched = True
PYOP
  corrupt default-backend-to-internal-port "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Service" and (d.get("metadata") or {}).get("name") == "user-management":
        d["spec"]["ports"] = [p for p in d["spec"]["ports"] if p.get("name") != "internal-auth"]
        touched = True
PYOP
  corrupt internal-port-not-published "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Deployment" and (d.get("metadata") or {}).get("name") == "user-management":
        for c in d["spec"]["template"]["spec"]["containers"]:
            for cp in c.get("ports") or []:
                if cp.get("name") == "internal-auth":
                    cp["containerPort"] = 9999
                    touched = True
PYOP
  corrupt pod-does-not-listen-on-the-internal-port "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
import json
touched = False
for d in docs:
    if d.get("kind") == "Secret" and "instance" in (d.get("stringData") or {}):
        cfg = json.loads(d["stringData"]["instance"])
        cfg["infrastructure"]["userManagement"]["internalPort"] = cfg["infrastructure"]["userManagement"]["port"]
        d["stringData"]["instance"] = json.dumps(cfg)
        touched = True
PYOP
  corrupt callers-dial-the-public-port "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Ingress" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        d["metadata"]["annotations"]["nginx.ingress.kubernetes.io/rewrite-target"] = "/x/$2"
        touched = True
PYOP
  corrupt sso-rewritten "$tmp/op.py"

  # And the suffix source: a constant the chart no longer matches must fail, since the
  # split between this check and the Go tests rests entirely on that one shared string.
  sed 's|/.well-known/oauth-protected-resource|/.well-known/moved|' \
    "$WELLKNOWN_GO" > "$tmp/wellknown-moved.go"
  if check "$tmp/full.yaml" "$tmp/wellknown-moved.go" "$SSO_GO" "$FRONT_END" >/dev/null 2>&1; then
    echo "self-test FAILED: the check passed a render whose route no longer matches the" >&2
    echo "suffix constant the services serve." >&2
    exit 1
  fi

  echo "self-test passed: the check fails on a missing route, a wrong pathType, a wrong"
  echo "  port name, a wrong backend, a rewritten path, a wrong host, missing TLS, a moved"
  echo "  suffix constant, a single sign-on callback sent to the wrong service or rewritten,"
  echo "  a snippet annotation on any Ingress, a route or default backend to the internal-auth"
  echo "  port, an unpublished internal-auth port, a pod that does not listen on it, and callers told"
  echo "  to dial the public port."
}

self_test_gateway() {
  FRONT_END=gateway
  # The same negative control for the Gateway API render: one corruption of the RENDERED
  # manifest per property the gateway branch asserts. A check that resolves requests the way
  # a controller does must be shown to fail for each way a request can go to the wrong place.
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp:-}"' RETURN
  render > "$tmp/full.yaml"

  if ! check "$tmp/full.yaml" "$WELLKNOWN_GO" "$SSO_GO" "$FRONT_END" >/dev/null; then
    echo "self-test (gateway): the unmodified render already fails, so a failure proves nothing." >&2
    exit 1
  fi

  corrupt() { # corrupt <name> <python-body-file>
    python3 - "$tmp/full.yaml" "$tmp/$1.yaml" "$2" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
ns = {"docs": docs, "sys": sys}
exec(open(sys.argv[3]).read(), ns)
if not ns.get("touched"):
    sys.exit("self-test: the corruption changed nothing, so the expected failure "
             "would be meaningless.")
yaml.safe_dump_all(docs, open(sys.argv[2], "w"))
PY
    if check "$tmp/$1.yaml" "$WELLKNOWN_GO" "$SSO_GO" "$FRONT_END" >/dev/null 2>&1; then
      echo "self-test (gateway) FAILED: the check passed a render corrupted by '$1'." >&2
      exit 1
    fi
  }

  # mutate_rule <route-name> <rule-match-prefix> <python statement on `rule`> : one rule, one defect.
  mutate_rule() {
    cat > "$tmp/op.py" <<PYOP
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "$1":
        for rule in d["spec"]["rules"]:
            for m in rule.get("matches") or []:
                if m["path"]["value"] == "$2":
                    $3
                    touched = True
PYOP
  }

  mutate_rule devicechain-wellknown /.well-known/oauth-protected-resource 'rule["matches"][0]["path"]["value"] = "/.well-known/nope"'
  corrupt wellknown-route-moved "$tmp/op.py"
  mutate_rule devicechain-wellknown /.well-known/oauth-protected-resource 'rule["matches"][0]["path"]["type"] = "Exact"'
  corrupt wellknown-exact-not-prefix "$tmp/op.py"
  mutate_rule devicechain-wellknown /.well-known/oauth-protected-resource 'rule["backendRefs"][0]["name"] = "frontend"'
  corrupt wellknown-wrong-backend "$tmp/op.py"
  mutate_rule devicechain-wellknown /.well-known/oauth-authorization-server 'rule["backendRefs"][0]["port"] = 9999'
  corrupt wellknown-wrong-port "$tmp/op.py"
  mutate_rule devicechain-wellknown /.well-known/oauth-authorization-server 'rule["filters"] = [{"type": "URLRewrite", "urlRewrite": {"path": {"type": "ReplacePrefixMatch", "replacePrefixMatch": "/"}}}]'
  corrupt wellknown-rewritten "$tmp/op.py"
  mutate_rule devicechain-api /api/mcp 'rule["filters"] = []'
  corrupt api-rewrite-removed "$tmp/op.py"
  mutate_rule devicechain-api /api/mcp 'rule["backendRefs"][0]["name"] = "frontend"'
  corrupt endpoint-wrong-backend "$tmp/op.py"
  mutate_rule devicechain-api /api/user-management 'rule["backendRefs"][0]["name"] = "frontend"'
  corrupt sso-wrong-backend "$tmp/op.py"
  # A longer prefix elsewhere steals the walk: the property the move to longest-match must keep.
  mutate_rule devicechain-api /api/mcp 'rule["matches"][0]["path"]["value"] = "/api/mcp/.well-known"'
  corrupt endpoint-prefix-shortened-so-another-route-wins "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "devicechain-wellknown":
        d["spec"]["hostnames"] = ["elsewhere.example.com"]
        touched = True
PYOP
  corrupt wellknown-wrong-host "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "devicechain-wellknown":
        d["spec"]["parentRefs"][0]["sectionName"] = "http"
        touched = True
PYOP
  corrupt wellknown-on-the-plain-listener "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Gateway":
        d["spec"]["listeners"] = [l for l in d["spec"]["listeners"] if l["name"] != "https"]
        touched = True
PYOP
  corrupt https-listener-removed "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Gateway":
        for l in d["spec"]["listeners"]:
            if l["name"] == "https":
                l["tls"]["certificateRefs"] = []
                touched = True
PYOP
  corrupt https-listener-without-certificate "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "devicechain-https-redirect":
        d["spec"]["rules"][0]["filters"] = []
        touched = True
PYOP
  corrupt http-listener-serves-instead-of-redirecting "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        d["metadata"].setdefault("annotations", {})["traefik.io/server-snippet"] = "#"
        touched = True
PYOP
  corrupt snippet-shaped-annotation "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "HTTPRoute" and (d.get("metadata") or {}).get("name") == "devicechain-api":
        for rule in d["spec"]["rules"]:
            if rule["matches"][0]["path"]["value"] == "/api/user-management":
                rule["backendRefs"][0]["port"] = 9090
                touched = True
    if d.get("kind") == "Service" and (d.get("metadata") or {}).get("name") == "user-management":
        for sp in d["spec"]["ports"]:
            if sp.get("name") == "internal-auth":
                sp["port"] = 9090
PYOP
  corrupt route-to-internal-port "$tmp/op.py"

  cat > "$tmp/op.py" <<'PYOP'
touched = False
for d in docs:
    if d.get("kind") == "Service" and (d.get("metadata") or {}).get("name") == "user-management":
        d["spec"]["ports"] = [p for p in d["spec"]["ports"] if p.get("name") != "internal-auth"]
        touched = True
PYOP
  corrupt internal-port-not-published "$tmp/op.py"

  sed 's|/.well-known/oauth-protected-resource|/.well-known/moved|' \
    "$WELLKNOWN_GO" > "$tmp/wellknown-moved.go"
  if check "$tmp/full.yaml" "$tmp/wellknown-moved.go" "$SSO_GO" "$FRONT_END" >/dev/null 2>&1; then
    echo "self-test (gateway) FAILED: the check passed a render whose route no longer matches the" >&2
    echo "suffix constant the services serve." >&2
    exit 1
  fi

  echo "self-test (gateway) passed: the check fails on a moved well-known prefix, an Exact match in"
  echo "  place of a prefix, a wrong backend or port, a rewritten well-known path, a lost API rewrite, a"
  echo "  wrong host, a route on the plain listener, a missing https listener or certificate, a plain-http"
  echo "  route that serves instead of redirecting, a snippet-shaped annotation, a route to the"
  echo "  internal-auth port, an unpublished internal-auth port, a moved suffix constant, and a"
  echo "  shortened prefix that lets another route win."
}

self_test() {
  self_test_nginx
  self_test_gateway
}

case "${1-}" in
  --self-test) self_test ;;
  "")
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    for FRONT_END in nginx gateway; do
      render > "$tmp/full.yaml"
      check "$tmp/full.yaml" "$WELLKNOWN_GO" "$SSO_GO" "$FRONT_END"
      render_guards
    done
    FRONT_END=nginx
    issuer_derivation_guards
    ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
