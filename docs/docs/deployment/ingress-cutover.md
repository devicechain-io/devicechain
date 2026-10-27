---
sidebar_position: 4.7
title: Moving to Traefik
---

# Moving an existing install to Traefik {#ingress-cutover}

New installs serve their host through **Traefik** and the Kubernetes **Gateway API**. Earlier releases used
`ingress-nginx`, which the Kubernetes project has retired. This page is for an install that already runs
`ingress-nginx`: the move is done in steps, and the host keeps answering throughout. A cluster with no
`ingress-nginx` needs none of it; `dcctl install` puts Traefik alone on it.

What each command does to a cluster that still has `ingress-nginx`:

- **`dcctl install`** leaves `ingress-nginx` serving and installs Traefik and the Gateway API definitions beside it.
  Traefik gets a load balancer of its own, so it has its own address.
- **`dcctl upgrade`** gives each instance a Gateway and HTTPRoutes, and keeps its Ingress objects beside them for
  as long as `ingress-nginx` is on the cluster. Both serve the host.
- **DNS** is yours to move, when you are ready.
- **`dcctl upgrade --drop-legacy-ingress`** stops rendering the instance's Ingress objects.
- **`dcctl install --remove-legacy-ingress`** removes `ingress-nginx`, and only once no Ingress object names its class.

## Before you start {#ingress-cutover-before}

- Know what is different at the edge. See [What changes at the edge](#ingress-cutover-differences).
- If the instance gets its certificate from an ACME issuer (`ingress.tls.selfSigned: false` and a `ClusterIssuer`),
  read [Certificates from an ACME issuer](#ingress-cutover-acme): it needs a change after DNS has moved, not before.
- Give `dcctl install` the settings the cluster was installed with, such as `--ha`, as well as the new `--version`,
  as for any [rolling upgrade](./releases-and-upgrades.md#rolling-upgrades).

## The steps {#ingress-cutover-steps}

1. **Install the new controller.** This leaves `ingress-nginx` serving.

   ```bash
   dcctl install <provider> --version <new-version>
   ```

2. **Upgrade each instance.** `--dry-run` first prints which front end the instance will have.

   ```bash
   dcctl upgrade <provider> <instance> --version <new-version> --dry-run
   dcctl upgrade <provider> <instance> --version <new-version>
   ```

   The instance now has a Gateway named `devicechain` in its namespace (`dci-<instance>`), with an `http` and an
   `https` listener, and HTTPRoutes behind it. Its Ingress objects are still there. The two have **separate
   certificates**: the Ingress objects keep `devicechain-tls`, which is what DNS still reaches, so the live host
   never waits for a new certificate; the Gateway uses `devicechain-tls-gateway`. The Gateway keeps that name
   for good, even after the Ingress objects are gone.

   The upgrade then checks the Gateway's certificate. See [The certificate gate](#ingress-cutover-cert-gate).

3. **Find Traefik's address** and check the instance through it, before DNS moves.

   ```bash
   kubectl -n traefik get service traefik
   kubectl -n dci-<instance> get gateway devicechain
   curl --resolve <host>:443:<traefik-address> https://<host>/
   ```

   The Service's `EXTERNAL-IP` (or hostname, on AWS) is Traefik's address. The Gateway should report
   `Programmed`. `--resolve` makes `curl` send the request to Traefik while still using your host name, so the
   certificate and the routes are the ones a browser would see. Add `-k` (or `--cacert` with the instance's
   CA, see [TLS, flags and a laptop install](./bootstrap.md#install-tls-and-flags)) when the instance is
   self-signed.

4. **Move DNS.** Point the host at Traefik's address. Nothing else is needed, and nothing has to be quick:
   the Ingress objects keep answering for as long as clients still resolve the old address.

5. **Drop the Ingress objects.** When traffic has moved, stop rendering them:

   ```bash
   dcctl upgrade <provider> <instance> --drop-legacy-ingress
   ```

   Do this for every instance on the cluster.

6. **Remove `ingress-nginx`.**

   ```bash
   dcctl install <provider> --remove-legacy-ingress
   ```

   Pass the same settings as in step 1. Removing the controller does not remove Ingress objects, and one left
   behind would stop answering without any fault being reported, so the command **refuses** while any exists:

   ```text
   ingress-nginx is still serving 1 Ingress object(s), and removing the controller leaves them in place with
   nothing to serve them, so every host they carry stops answering:
     dci-beta/devicechain-api
   Move each instance to the Gateway first: point DNS at the new controller, then run
   `dcctl upgrade <provider> <instance> --drop-legacy-ingress` for it. Nothing has been changed
   ```

   The list names every Ingress served by the `ingress-nginx` class, in any namespace, including ones you made
   yourself. Move or delete those too. The Ingress the install made for Grafana does not count: it moves to
   Traefik in the same apply.

### On a local cluster {#ingress-cutover-local}

A local (kind) cluster publishes a node's ports 80 and 443, and only one controller can hold them. While both controllers are installed, `ingress-nginx` holds the ports and Traefik is reachable only on a NodePort.

On a local cluster there is no DNS to move: the browser reaches whichever controller holds the node's ports. Steps 3 and 4 (the external address and DNS) do not apply, and a plain `dcctl upgrade` already moves an instance that has Ingress objects into the window. Run steps 5 and 6 back to back: the host does not answer from `--drop-legacy-ingress` until the removal has handed the ports to Traefik, and expect a short gap while that happens.

A **fresh** local cluster has no `ingress-nginx`, and Traefik holds the ports from the start. On a local cluster that has both, a **new** instance is served by `ingress-nginx` until the removal.

### Several instances {#ingress-cutover-several}

The cluster step is shared and each instance moves on its own schedule. `ingress-nginx` stays until the last
instance has run `--drop-legacy-ingress`. A cluster whose instances use different hosts needs DNS moved for each.

## The certificate gate {#ingress-cutover-cert-gate}

A Gateway with no certificate answers `https` with the controller's default certificate, so the move checks the Gateway's certificate at four points. The checks apply when the instance terminates TLS and the cluster has cert-manager; `--no-tls` skips them. A Gateway whose Secret you supply yourself (it carries no cert-manager issuer annotation and no `kubernetes.io/tls-acme: "true"`) has nothing to wait for and passes.

- **During the window,** `dcctl upgrade` waits 30 seconds for the certificate, then **warns and continues**. The Ingress objects still serve the host, and many issuers can only issue the Gateway's certificate once DNS points at Traefik.
- **`dcctl upgrade --drop-legacy-ingress`** refuses, before it renders or changes anything, unless the Gateway's cert-manager Certificate is `Ready` and controlled by the Gateway. It also refuses when the instance has no Gateway yet. Move DNS first so the certificate can be issued, check `kubectl -n dci-<instance> get certificate`, and run the command again.
- **`dcctl install --remove-legacy-ingress`** checks the Gateway of every instance on the cluster, and names each one whose certificate is not `Ready`. Removing `ingress-nginx` does not change how those instances are served; the check keeps a broken Gateway certificate from going unnoticed until nothing else serves the host.
- **An upgrade that leaves an instance on the Gateway alone** waits up to five minutes, and fails with the reason. The services are already up when it fails.

A refusal ends with "Nothing has been changed". Fix what it names and **run the same command again**. The common causes:

- **A Certificate exists for the Secret, but the Gateway does not control it.** cert-manager will not adopt a Certificate it did not create for the Gateway, so one left over from an Ingress, or made by hand, under the same Secret name leaves the Gateway with none. The message prints the command that deletes it (`kubectl -n dci-<instance> delete certificate <name>`); the Gateway's own is then created.
- **No Certificate is created at all.** cert-manager's Gateway API support is not on, or it started before the Gateway API definitions existed. `kubectl -n cert-manager logs deploy/cert-manager` says. A `dcctl install` of this release turns it on.
- **The Certificate is not `Ready`.** The message carries the issuer's own text. With an ACME issuer, see below.

## Certificates from an ACME issuer {#ingress-cutover-acme}

An HTTP-01 issuer that solves through the `nginx` ingress class keeps working during the window, for the Gateway's certificate too: the challenge is served by `ingress-nginx`, where DNS still points. Leave it as it is until DNS has moved. Then, before `--drop-legacy-ingress` and `--remove-legacy-ingress`, give it a `gatewayHTTPRoute` solver, or its renewals fail once `ingress-nginx` is gone. A `gatewayHTTPRoute` solver set before DNS moves fails, because the challenge reaches `ingress-nginx`, which has no route for it. A DNS-01 solver works throughout.

The `gatewayHTTPRoute` solver names the `http` listener of the instance's Gateway, `devicechain`:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    # server, email and privateKeySecretRef as before
    solvers:
      - http01:
          gatewayHTTPRoute:
            parentRefs:
              - group: gateway.networking.k8s.io
                kind: Gateway
                name: devicechain
                sectionName: http
```

The `parentRef` has no namespace, and one covers every instance: cert-manager creates the solver's route in the namespace of the Certificate being issued, so it attaches to that instance's `devicechain` Gateway. The chart's redirect from `http` to `https` is a less specific match than the challenge path, so it does not intercept the challenge.

## Rolling back {#ingress-cutover-rollback}

While `ingress-nginx` is still on the cluster, an instance can be put back on it:

```bash
dcctl upgrade <provider> <instance> --ingress-controller nginx
```

The choice is **recorded on the instance** and kept by every later upgrade, because an upgrade re-derives
everything it renders and a rollback that a plain upgrade undid would be no rollback. `--ingress-controller gateway`
records the other choice. `--ingress-controller auto` clears the record, and the instance is then decided from the
cluster again: the Gateway where Traefik runs.

- `nginx` needs `ingress-nginx` on the cluster, and `gateway` needs Traefik. Each is refused, with nothing changed,
  when the controller it names is missing.
- `--ingress-controller nginx` and `--drop-legacy-ingress` together are refused: one makes the Ingress objects the
  only front end and the other stops rendering them.
- If DNS already points at Traefik, point it back at `ingress-nginx`'s address: the rollback removes the Gateway.
- Once `ingress-nginx` has been removed there is no rollback to it, and an install does not bring a removed
  controller back.

## What changes at the edge {#ingress-cutover-differences}

The Gateway serves the same hosts, paths and backends as the Ingress objects did. What differs is who configures
the edge:

- **Annotations no longer apply.** `ingress.className`, `ingress.annotations` and `ingress.uploadMaxBodySize` in a
  [values overlay](./tuning-an-instance.md) belong to the Ingress objects. On an instance served by the Gateway
  alone, a value you type for them in a run is **refused**, and one the instance already recorded is dropped from
  the render with a warning and kept in the record, so it applies again under `--ingress-controller nginx`.
- **Uploads are not capped or buffered.** Traefik streams the request body, and the update service enforces its
  own ceiling. Traefik's read timeout bounds the **whole request**, body included, so it is derived from the
  largest artifact the cluster accepts: the OpenTofu variable `traefik_max_artifact_bytes`, 512 MiB by default,
  gives a window of 2,228 seconds. Raise it **before** raising an instance's `updates.maxArtifactBytes`. The same
  window is how long a client sending headers slowly can hold a connection, against 60 seconds before.
- **Download query strings stay out of the access log**, which is off by default. The download capability travels
  in `?cap=`.
- **Paths are case-sensitive and end on a path boundary.** `/API/<area>` and `/api/<area>X` fall through to the
  console instead of reaching the area.
- **Client addresses.** Traefik writes the real peer into `X-Forwarded-For` and its Service uses
  `externalTrafficPolicy: Local`; see [Client addresses and trusted proxies](./bootstrap.md#trusted-proxies). On AWS a Classic Load Balancer hides the client address. `dcctl install --aws-nlb traefik` gives Traefik's Service a Network Load Balancer, and it is the value to use during the window because `ingress-nginx` keeps serving on its own load balancer. Changing a Service replaces its load balancer and changes its hostname, so repoint DNS afterwards.

## Related {#ingress-cutover-related}

- [`dcctl install`](../reference/dcctl.md) and [`dcctl upgrade`](../reference/dcctl.md) list every flag used here.
- [Releases & upgrades](./releases-and-upgrades.md#self-registration-upgrade) has the notes for the release that
  ships this change.
