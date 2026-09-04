---
title: Your First Device
---

# Your first device, end to end

By the end of this page a device you created will have sent a reading, and you will be
looking at that reading in the console. No hardware, no firmware — the "device" is a `curl`
command, which is all a device is from the platform's side.

Budget about half an hour, most of it waiting for the bootstrap.

:::note What this page assumes
`dcctl`, a Kubernetes cluster (1.29 or newer) you can reach with `kubectl`, and
[OpenTofu](https://opentofu.org/) (`tofu`) on your `PATH`. `dcctl` does not create the cluster
— [kind](https://kind.sigs.k8s.io/) is the usual choice locally. Everything else it carries
inside itself. `dcctl preflight local` checks all of this before you start, and the
[bootstrap guide](../deployment/bootstrap.md#prerequisites) has the detail.

Commands below assume the instance is reachable at `localhost` over plain HTTP, which is what
the flags in step 1 produce.
:::

## 1. Bring up an instance

```bash
dcctl bootstrap local devicechain --host localhost --no-tls
```

The instance id — `devicechain` here — is not decoration. It becomes the namespace, and it is
the first segment of every device topic and ingest path on this page. If you choose a
different one, substitute it throughout.

When the bootstrap finishes it prints the namespace, the console URL, and the superuser
credential. The default superuser is `superuser@devicechain.local` with the password
`devicechain`.

Open the console at `http://localhost/` and sign in. It will be empty — there is no tenant
yet, and every device belongs to one.

## 2. Create a tenant

A tenant is instance-level administration, so rather than walk the admin API by hand, use the
command that does the whole dance in one step:

```bash
dcctl sim create demo
```

That mints a tenant `sim-demo`, creates an identity `demo@sim.devicechain.local` scoped to it
with the tenant-admin role and no instance-wide power, and writes a handshake file at
`~/.devicechain/sims/demo.json`. Read your identity's generated password out of it:

```bash
cat ~/.devicechain/sims/demo.json
```

The `simPassword` field is the password for `demo@sim.devicechain.local`. You will use both in
the next step.

:::tip This command exists to run simulations, and it is useful here for its side effect
`dcctl sim create` is really the first half of the [simulator](#where-to-go-next) workflow. We
are borrowing it because minting a tenant plus a scoped identity is exactly what you need, and
doing it by hand means three mutations on the instance admin API. Everything after this step
is the ordinary tenant API that any application uses.
:::

## 3. Get a tenant token

Authentication is two calls. The first proves who you are; the second picks which tenant you
are acting in, because one person can belong to several.

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}",
       "variables":{"e":"demo@sim.devicechain.local","p":"<simPassword from step 2>"}}'
```

That returns an `identityToken` — it says who you are, and nothing about where you are acting.
Exchange it for a tenant-scoped `accessToken`:

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}",
       "variables":{"t":"<identityToken>","n":"sim-demo"}}'
```

Keep that `accessToken`. Every call from here carries it:

```bash
export DC_TOKEN='<accessToken>'
```

## 4. Create the device

Devices are typed, so a device type comes first. Everything is addressed by a **token** you
choose — a stable, human-readable handle — rather than by a generated id.

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

Now give it a credential. This is what the device presents to prove it is itself; the platform
expects one by default.

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
                         "enabled":true}}}'
```

Pick your own `credentialId` — any unguessable string. For an `ACCESS_TOKEN` credential the
`credentialId` **is** the secret the device presents, so treat it like a password rather than
like a name.

Refresh the console's **Devices** list and `sensor-001` is there, with no data yet.

## 5. Open a path to the ingest endpoint

Device traffic does not go through the same door as the API. The ingress publishes the console
and `/api/…`; the device-ingest listener is a separate port that a stock install does **not**
expose outside the cluster. Forward it:

```bash
kubectl -n devicechain port-forward svc/event-sources 8081:8081
```

Leave that running in its own terminal.

:::note Why this step exists
This is a property of the default install, not of your setup: making a fleet's ingest endpoint
publicly reachable is a decision an operator should make on purpose, so nothing makes it for
you. A real deployment exposes it deliberately; for one `curl` from your laptop, a port-forward
is the smaller thing to do.
:::

## 6. Send a reading

This is the device.

```bash
curl -i -X POST http://localhost:8081/devicechain/sim-demo/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001",
       "eventType":"Measurement",
       "credentialType":"ACCESS_TOKEN",
       "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
       "payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

`202 Accepted` means the event was queued. Two things about that body are worth noticing now,
because they catch nearly everyone once:

- **Every payload wraps its readings in `entries`**, even a single one.
- **Every numeric value is a JSON string.** `"21.5"`, not `21.5`. A bare number is rejected.

The path is `/{instanceId}/{tenant}/events` — `devicechain` is the instance from step 1 and
`sim-demo` is the tenant from step 2. A `404` here almost always means one of those two is
wrong.

Send a few more with different values, so there is a line to look at rather than a point:

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

## 7. See your data

**In the console**, open `http://localhost/devices/sensor-001`. The device now shows as active,
with `temperature` and its latest value.

**Over the API**, the same thing:

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

That is a device, end to end: registered, credentialed, reporting, and queryable.

## If something did not work

| What you see | Usually means |
| --- | --- |
| `404` from the ingest `POST` | The instance id or tenant in the path is wrong. They are `devicechain` and `sim-demo` unless you changed them. |
| Connection refused on `:8081` | The port-forward in step 5 is not running. |
| `400` from the ingest `POST` | A bare number instead of a string, or readings not wrapped in `entries`. |
| The event is accepted but nothing appears | The credential did not match. `credentialId` in the body must be exactly the one you created in step 4. |
| `429` from the ingest `POST` | The tenant is over its ingest rate limit — you are sending faster than its tier allows. |
| Unauthorized on an API call | The access token has expired, or you are sending the `identityToken` from the first call in step 3 instead of the `accessToken` from the second. |

## Where to go next {#where-to-go-next}

- **One device is not a fleet.** `dcctl sim create` from step 2 also set up a simulated
  scenario. Build and run the simulator to have it provision a populated tenant and emit
  continuously:

  ```bash
  cd backend/sims/dc-simulator && make build
  ./build/dc-simulator --handshake ~/.devicechain/sims/demo.json
  ```

  Then `dcctl sim status demo`, `dcctl sim stop demo`, `dcctl sim start demo`. Note that the
  simulator reaches the same ingest endpoint, so it needs the port-forward from step 5 too.

- **[Connecting a device](../guides/connecting-a-device.md)** — the real transport, MQTT, with
  the credential on the connection as well as the event, plus every payload shape and the rules
  the pipeline enforces.
- **[Transport capability matrix](../reference/transport-matrix.md)** — what each transport
  supports in each direction before you commit to one.
- **[Sending a command](../guides/sending-commands.md)** — the other direction.
- **[Event processing](../concepts/event-processing.md)** — turning those readings into alarms.

## Cleaning up

```bash
dcctl sim destroy demo
dcctl destroy local devicechain
```
