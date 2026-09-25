---
title: Your First Device
---

# Your First Device {#your-first-device-end-to-end}

By the end of this page, a device you created has sent a reading and you are looking at that
reading in the console. You need no hardware and no firmware. The "device" is a `curl` command,
which is all a device is from the platform's side.

Budget about half an hour, most of it waiting for the bootstrap.

:::note What this page assumes
You need `dcctl` plus five tools on your `PATH`: `docker`, `kubectl`, `helm`,
[`kind`](https://kind.sigs.k8s.io/) and [OpenTofu](https://opentofu.org/) (the `tofu` binary;
`terraform` also works). You do not need a cluster in advance. The details, and why `helm` is on
the list, are under [Prerequisites](#prerequisites) below.
:::

## Prerequisites {#prerequisites}

`dcctl install` and `dcctl bootstrap` each run a preflight first, and **stop** if one of the five
tools is missing. A gap costs you the first ten seconds rather than ten minutes.

- `helm` is required. `dcctl` carries the chart inside itself and installs it through Helm's
  Go library rather than the command, but the preflight checks for the binary regardless.
- `ko` and `cloud-provider-kind` are only warnings. You need `ko` solely to build images from
  source (`--build`).
- No cluster is needed in advance. `dcctl install local` looks for a kind cluster named
  `devicechain` (or the name given with `--cluster`) and offers to create one if there is none.
  `--kube-context <name>` points it at a cluster you already run, which it will never create or
  delete.
- Kubernetes **1.29 or newer**, either way. Older versions are refused, because the database
  charts refuse them.

`dcctl preflight local` runs exactly these checks without bootstrapping anything. The
[bootstrap guide](../deployment/bootstrap.md#prerequisites) has the detail.

The commands below assume the instance is reachable at `localhost` over plain HTTP, which is what
the flags in step 1 produce.

## 1. Bring up an instance {#1-bring-up-an-instance}

Prepare the cluster once, then create the instance on it:

```bash
dcctl install local
dcctl bootstrap local devicechain --host localhost --no-tls
```

`dcctl install` creates the kind cluster and installs what every instance on it shares: the
DeviceChain operator and its custom resource definitions, the relational database, the
CloudNativePG operator, cert-manager, monitoring and ingress. You run it once per cluster, and
`dcctl bootstrap` refuses on a cluster where it has not completed. See
[Install the cluster](../deployment/bootstrap.md#install).

The instance id (`devicechain` here) matters in two places:

- It names the instance's Kubernetes namespace, as the id behind a `dci-` prefix
  (`dci-devicechain`).
- It is the first segment of every device topic and ingest path on this page.

If you choose a different id, substitute it throughout: on its own in the topics and paths, and
after the `dci-` prefix wherever a command names the namespace.

When the bootstrap finishes, it prints the namespace, the console URL and the superuser
credential. The superuser is `superuser@devicechain.local`. There is no default password: the
bootstrap generates one for this instance and prints it once, at the end of its output. To read
it again later:

```bash
kubectl -n dci-devicechain get secret dci-devicechain-superuser -o jsonpath='{.data.password}' | base64 -d
```

That Secret keeps the password the superuser was **first** given. If you change the password in
the console, the Secret is not updated.

Open the console at `http://localhost/` and sign in. It is empty, because there is no tenant yet
and every device belongs to one.

## 2. Create a tenant {#2-create-a-tenant}

Creating a tenant is instance-level administration. Rather than walk the admin API by hand, use
the command that does it in one step:

```bash
dcctl sim create demo
```

This command:

- mints a tenant `sim-demo`,
- creates an identity `demo@sim.devicechain.local` scoped to it, with the tenant-admin role and no
  instance-wide power, and
- writes a handshake file at `~/.devicechain/sims/demo.json`.

Read your identity's generated password out of the handshake file:

```bash
cat ~/.devicechain/sims/demo.json
```

The `simPassword` field is the password for `demo@sim.devicechain.local`. You use both in the next
step.

:::tip Borrowed from the simulator
`dcctl sim create` is the first half of the [simulator](#where-to-go-next) workflow. It is used
here because it mints a tenant plus a scoped identity, which is exactly what you need; doing that
by hand takes three mutations on the instance admin API. Everything after this step is the
ordinary tenant API that any application uses.
:::

## 3. Get a tenant token {#3-get-a-tenant-token}

Authentication takes two calls. The first proves who you are. The second picks which tenant you
are acting in, because one person can belong to several.

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}",
       "variables":{"e":"demo@sim.devicechain.local","p":"<simPassword from step 2>"}}'
```

That returns an `identityToken`. It says who you are, and nothing about where you are acting.
Exchange it for a tenant-scoped `accessToken`:

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}",
       "variables":{"t":"<identityToken>","n":"sim-demo"}}'
```

Keep that `accessToken`. Every call from here on carries it:

```bash
export DC_TOKEN='<accessToken>'
```

## 4. Create the device {#4-create-the-device}

Devices are typed, so you create a device type first. Everything is addressed by a **token** you
choose, a stable, human-readable handle, rather than by a generated id.

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}",
       "variables":{"r":{"token":"temp-probe","name":"Temperature probe"}}}'
```

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001","deviceTypeToken":"temp-probe","name":"Bench sensor"}}}'
```

Now give the device a credential. The credential is what the device presents to prove it is
itself, and the platform expects one by default.

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
                         "enabled":true}}}'
```

Pick your own `credentialId`: any unguessable string. For an `ACCESS_TOKEN` credential, the
`credentialId` **is** the secret the device presents, so treat it like a password rather than a
name.

Refresh the console's **Devices** list. `sensor-001` is there, with no data yet.

## 5. Open a path to the ingest endpoint {#5-open-a-path-to-the-ingest-endpoint}

Device traffic does not go through the same door as the API. The ingress publishes the console and
`/api/…`. The device-ingest listener is a separate port that a stock install does **not** expose
outside the cluster. Forward it:

```bash
kubectl -n dci-devicechain port-forward svc/event-sources 8081:8081
```

Leave that running in its own terminal.

:::note Why this step exists
This is a property of the default install, not of your setup. Making a fleet's ingest endpoint
publicly reachable is a decision an operator should make on purpose, so nothing makes it for you.
A real deployment exposes it deliberately; for one `curl` from your laptop, a port-forward is the
smaller thing to do.
:::

## 6. Send a reading {#6-send-a-reading}

This `curl` is the device:

```bash
curl -i -X POST http://localhost:8081/devicechain/sim-demo/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001",
       "eventType":"Measurement",
       "credentialType":"ACCESS_TOKEN",
       "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
       "payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

`202 Accepted` means the event was queued. Two rules in that body catch nearly everyone once:

- **Every payload wraps its readings in `entries`**, even a single one.
- **Every numeric value is a JSON string:** `"21.5"`, not `21.5`. A bare number is rejected.

The path is `/{instanceId}/{tenant}/events`. `devicechain` is the instance from step 1 and
`sim-demo` is the tenant from step 2. A `404` here means the **instance id** is wrong, because the
route exists only under this instance's own id.

A wrong **tenant** does not return `404`. Any well-formed tenant name is accepted with `202`,
whether or not a tenant of that name exists. The event is dropped further downstream, and nothing
in the response says so. If a `202` produces no data, check the tenant name before anything else.

Send a few more readings with different values, so there is a line to look at rather than a
point:

```bash
for t in 21.9 22.4 22.1 23.0; do
  curl -s -o /dev/null -X POST http://localhost:8081/devicechain/sim-demo/events \
    -H 'Content-Type: application/json' \
    -d "{\"device\":\"sensor-001\",\"eventType\":\"Measurement\",
         \"credentialType\":\"ACCESS_TOKEN\",
         \"credentialId\":\"5f989616-2a0d-4160-8ae1-da5fad2898b2\",
         \"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\"}}]}}"
  sleep 1
done
```

## 7. See your data {#7-see-your-data}

In the console, open `http://localhost/devices/sensor-001`. The device now shows as **Online**,
with `temperature` and its latest value. Nothing declared it online: for a device on HTTP,
presence is inferred from the fact that an event arrived.

Over the API, the same latest values:

```bash
curl -s -X POST http://localhost/api/device-state/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"sensor-001\"){name value unit occurredTime}}"}'
```

And the history rather than the last value:

```bash
curl -s -X POST http://localhost/api/event-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{measurementEvents(criteria:{pageNumber:1,pageSize:20,deviceToken:\"sensor-001\"}){results{name value occurredTime} pagination{totalRecords}}}"}'
```

You now have a device end to end: registered, credentialed, reporting and queryable.

## Troubleshooting {#if-something-did-not-work}

| What you see | Usually means |
| --- | --- |
| `404` from the ingest `POST` | The **instance id** in the path is wrong. It is `devicechain` unless you changed it. A wrong tenant does not produce this. |
| Connection refused on `:8081` | The port-forward in step 5 is not running. |
| `400` from the ingest `POST` | A bare number instead of a string, readings not wrapped in `entries`, or a tenant segment that is not a valid token. |
| `202`, but nothing appears | Either the **tenant** does not exist (a well-formed name is accepted whether or not it names anything), or the credential did not match. `credentialId` in the body must be exactly the one you created in step 4. |
| `429` from the ingest `POST` | The tenant is over its ingest rate limit: you are sending faster than its tier allows. The event was not accepted. The response carries a `Retry-After` header, so back off and send it again. |
| `503` from the ingest `POST` | The event could not be handed to the stream, and it was **not** stored. Retry it. Apart from `429` after backing off, the other statuses are terminal for that request. |
| Unauthorized on an API call | The access token has expired, or you are sending the `identityToken` from the first call in step 3 instead of the `accessToken` from the second. |

## Where to go next {#where-to-go-next}

- **Run a simulated fleet.** One device is not a fleet. `dcctl sim create` from step 2 also set up
  a simulated scenario. Build and run the simulator to have it provision a populated tenant and
  emit continuously:

  ```bash
  cd backend/sims/dc-simulator && make build
  ./build/dc-simulator --handshake ~/.devicechain/sims/demo.json
  ```

  Then control it with `dcctl sim status demo`, `dcctl sim stop demo` and `dcctl sim start demo`.
  The simulator reaches the same ingest endpoint, so it needs the port-forward from step 5 too.

- **[Connecting a device](../guides/connecting-a-device.md)**: the real transport, MQTT, with the
  credential on the connection as well as the event, plus every payload shape and the rules the
  pipeline enforces.
- **[Transport capability matrix](../reference/transport-matrix.md)**: what each transport
  supports in each direction, before you commit to one.
- **[Sending a command](../guides/sending-commands.md)**: the other direction.
- **[Event processing](../concepts/event-processing.md)**: turning those readings into alarms.

## Cleaning up {#cleaning-up}

```bash
dcctl sim destroy demo
dcctl destroy local devicechain
```

`dcctl destroy` removes the instance and leaves the cluster installed, ready for the next
bootstrap. It waits until the instance's namespace is fully gone, so the cluster is ready at once
for a bootstrap under the same name. If it is interrupted, running it again finishes the job.

:::warning Move the escrow artifact first
Before you bootstrap again under the same name, move aside the escrow artifact that `destroy`
names. The next instance mints a key of its own, and bootstrap will not write over the old one.
:::

To remove the cluster as well:

```bash
kind delete cluster --name devicechain
```
