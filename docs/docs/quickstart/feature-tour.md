---
title: Feature Tour
---

# Feature Tour {#feature-tour}

[Your first device](./first-device.md) stops at the first reading. This tour keeps going. By the end
of it one site is running from end to end: a device type and its profile, two devices reporting
readings, a dashboard, a rule that raises an alarm and sends a command, the device's answer to that
command, and a **customer user** who signs in and sees only their own device.

Then you run the same tour a second time, as a second tenant with a different customer, by changing
one parameters file. Nothing is forked.

The tour is a set of shell scripts in the repository under
[`examples/feature-tour`](https://github.com/devicechain-io/devicechain/tree/main/examples/feature-tour).
They call the same public GraphQL API and the same `dcctl` that you would use yourself, and every
step below also says where the console does the same thing. DeviceChain has no import for a whole
site: the scripts are ordinary API calls that you can read and change.

Budget about 45 minutes once an instance is up.

## What you need {#what-you-need}

- A running instance and its console. [Your first device, step 1](./first-device.md#1-bring-up-an-instance)
  brings one up on a local kind cluster. You do not need its tenant or its device; the tour creates
  its own.
- `bash`, `curl`, `jq`, `openssl`, `docker` and `kubectl` on your `PATH`. On Windows use Git Bash.
- A checkout of the repository, to get the scripts:

  ```bash
  git clone https://github.com/devicechain-io/devicechain
  cd devicechain/examples/feature-tour
  ```

Three settings say where your instance is. Each has a default that matches a plain
`dcctl install local` followed by `dcctl bootstrap local tour --host localhost --no-tls`, and each can
be overridden from the environment:

| Variable | Default | Set it when |
| --- | --- | --- |
| `DC_URL` | `http://localhost` | The console is served on another host or port. |
| `INSTANCE` | `tour` | Your `dcctl` instance has another name. It names the namespace `dci-<instance>`, which the scripts read the broker's certificate authority and the superuser's password from. |
| `MQTT_HOST` | `devicechain-control-plane` | Your kind cluster is not named `devicechain`. It is the cluster node's container name, `<cluster>-control-plane`. |

If your `kubectl` has more than one context, also set `KUBE_CONTEXT`.

```bash
export DC_URL=http://localhost INSTANCE=devicechain MQTT_HOST=devicechain-control-plane
```

The simulated devices are containers on the `kind` Docker network, so they reach the broker's MQTT
node port by the node's name. On a cluster that is not kind, run
[`device/coldroom.sh`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/device/coldroom.sh)
from any machine that can reach the broker, or point your own client at it. See
[Connecting a device](../guides/connecting-a-device.md).

## Run it {#run-it}

```bash
./tour.sh sites/site-a.env all
```

That provisions everything, starts the two devices, waits for the rule to act, and then checks each
claim this page makes, printing `PASS` or `FAIL` for each. The checks end with
`tour tour-a: all checks passed`. The two halves can be run separately:

```bash
./tour.sh sites/site-a.env up       # provision, dashboards, customer user, start the devices
./tour.sh sites/site-a.env verify   # wait for the loop, then check the operator's and the customer's views
```

The scripts are safe to run again. Anything that exists is left alone, and a dashboard whose
definition changed is updated and published as a new version.

The passwords the scripts generate (the site operator's, the customer user's and the devices') are
written to `.state/<tenant>/`, which is git-ignored and readable only by you. They are never printed.

The sections below walk through what each script did, and show the same step in the console.

## 1. The tenant and its administrator {#1-the-tenant}

`01-provision.sh` starts at the instance level. Using the superuser's password from the Secret that
`dcctl bootstrap` created, it creates the tenant (`createTenant` on the admin API) and an identity for
the site's own administrator, `operator@coldchain-a.example`, who is a `tenant-admin` of that tenant
and of nothing else.

Everything after this runs as that administrator, which is who you sign in as: the superuser's
console is the instance **Admin** console, with no tenant pages in it. In the console, the same work
is **Tenants → New tenant** and **Identities** in the Admin console.

To sign in to the tenant console, open `DC_URL`, enter `operator@coldchain-a.example`, and use the
password in `.state/tour-a/operator-password`.

## 2. A device type and its profile {#2-device-type-and-profile}

A **device type** classifies devices, and its **profile** is the contract: which measurements a
device reports, which commands it accepts, and which rules watch its readings. The script creates
the profile `cold-room` with:

- the measurements `temperature` and `humidity`,
- one command, `boost-cooling`, and
- one detection rule, `cold-room-too-warm`: when `temperature` goes above the site's line, raise a
  major alarm **and** send `boost-cooling`. The rule is [`rule.json`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/rule.json),
  which is what the console's rule form saves.

It then publishes the profile and creates the device type `cold-room` on it. **Publishing matters:**
rules reach live readings only when the profile is published, and the platform compiles the rule at
that moment, so a rule it cannot compile is refused there rather than failing silently later. A
profile that declares commands accepts only those commands.

In the console: **Devices → Device Profiles** (rules, metrics and commands) and **Devices → Device
Types**. See [Device types and profiles](/user-guide/device-types-and-profiles) and
[Detection rules](/user-guide/detection-rules).

## 3. Devices {#3-devices}

The script creates two devices of the type, `coldroom-a1` and `coldroom-a2`, and gives each an
`MQTT_BASIC` credential. A device presents the username `<tenant>:<credentialId>` and its password;
the platform stores only a digest of the password.

In the console: **Devices → Devices**, then the **Credentials** tab of a device. See
[Devices](/user-guide/devices) and [Device credentials](../guides/device-credentials.md).

## 4. A customer, and which device is theirs {#4-a-customer}

The script creates a customer type and the customer `acme`, and **assigns** `coldroom-a1` to it: a
relationship of type `assigned` from the device to the customer. `coldroom-a2` is deliberately left
unassigned, in the same tenant. That is what lets the last step show the customer seeing one device
while the operator sees both.

In the console: **Organization → Customers**, and the device's **Assignment** tab. See
[Assets, customers and areas](/user-guide/assets-customers-areas) and
[Managing assignments](../guides/managing-assignments.md).

## 5. Readings {#5-readings}

```bash
./02-devices.sh sites/site-a.env up
```

starts one container per device. Each is a few lines of shell around `mosquitto_pub` and
`mosquitto_sub`. The cold room warms by one degree every four seconds, publishes `temperature` and
`humidity`, and keeps a subscription open for commands. It knows nothing about the rule, the alarm or
the dashboard: it reports and obeys. The subscription is also what shows the device online.

Within seconds the readings are in the console (the device's page, with the latest values) and over
the API:

```bash
curl -s "$DC_URL/api/device-state/graphql" -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"coldroom-a1\"){name value unit}}"}'
```

`$DC_TOKEN` is a tenant access token. [Your first device, step 3](./first-device.md#3-get-a-tenant-token)
shows the two calls that get one.

## 6. Your first dashboard {#6-first-dashboard}

A dashboard is a JSON definition of widgets bound to a **slot**, such as "the device being looked at".
There are two ways to make the first one, and the tour uses both.

**In the console, from the device type.** Open **Devices → Device Types → Cold room**, choose the
**Profile** tab, and choose **Create dashboard**. The drawer reads what the profile declares and
previews a board: a device selector, a card for each measurement, a chart, an alarm table and a
button for each command that is safe to send from a board. Give it a token, choose **Create draft and
open editor**, and the board opens with live data. It is a **draft**: viewers see nothing until you
publish it from the editor's **History** panel. See
[Creating a dashboard from a device type](/user-guide/dashboards#from-a-device-type).

**From a script.** `03-dashboards.sh` builds the same kind of board from
[`dashboard.jq`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/dashboard.jq)
and creates it with `createDashboard`, publishes it with `publishDashboard`, and does it twice:

- `tour-a-ops`, for the operator, with a **Send Boost cooling** button; and
- `tour-a-customer`, without the button, which it **shares** with the customer `acme`
  (`shareDashboard`). A member limited to a customer sees a dashboard only when it is shared with
  that customer and published.

Open `DC_URL/dashboards/tour-a-ops`. The two boards differ in nothing but the button, which is the
point of generating them from one definition.

## 7. The rule raises an alarm {#7-the-rule-raises-an-alarm}

Wait about half a minute. When a room passes the line, the rule fires: an alarm appears under
**Monitor → Alarms** (and on the board's alarm table and count), and it **clears by itself** when the
reading falls back. Nothing authors the clear: a rule is edge-triggered, and its alarm lasts as long
as its condition holds. The alarm list keeps one row for each alarm key on each device, showing its
latest raise.

The room warms again after it has cooled, so the cycle repeats: one alarm and one command for each
excursion. See [Alarms](/user-guide/alarms) and [Event processing](../concepts/event-processing.md).

## 8. The command and its outcome {#8-command-outcome}

The same firing sent the command `boost-cooling`. The device received it on its command topic,
cooled, and **answered**, with the command's own token and the delivery's nonce. The answer is what
moves the command from `SENT` to `SUCCESSFUL`. A device that obeys but never answers leaves the
command `SENT` until it times out.

`05-verify.sh` also sends one command of its own, the way you would:

```graphql
mutation {
  createCommand(request: {
    token: "my-first-command",
    deviceToken: "coldroom-a1",
    name: "boost-cooling",
    payload: "{\"targetC\":3}"
  }) {
    command { token status }
    rejection { code reason }
  }
}
```

It comes back `QUEUED`, and a few seconds later `SUCCESSFUL`:

```graphql
query {
  commandsByToken(tokens: ["my-first-command"]) { status outcome responsePayload }
}
```

Check `rejection` as well as the errors: a refused command is a successful response carrying a
`rejection`. In the console, the device page's **Commands** tab lists the history, and the board's
button sends one. See [Sending a command](../guides/sending-commands.md) and
[Commands](../concepts/commands.md#command-lifecycle).

## 9. The customer's view {#9-customer-view}

`04-customer-user.sh` invites `ops@acme.example` as a member of the tenant with the built-in read-only
`viewer` role and an access scope of **one customer**, `acme`, and then accepts the invitation with a
generated password. In the console you do this under **Access → Members → Invite member**, which
shows a one-time invitation link that you hand over yourself. The script accepts it so that the tour
can run unattended.

Sign in as `ops@acme.example` (the password is in `.state/tour-a/customer-password`). The user sees:

- a sidebar of **Dashboards**, **Alarms** and **Devices**, and nothing else;
- one device, `coldroom-a1`;
- one dashboard, `tour-a-customer`, with live readings and the alarm for their own room;
- no sign of `coldroom-a2`, its readings, its alarms or the operator's board.

The tenant chip at the top right reads `Acme Foods · viewer`. `05-verify.sh` checks each of these as
the customer user, and also that sending a command is refused. See
[Members, roles and customer users](/user-guide/members-roles-and-customer-users).

## What the checks print {#what-the-checks-print}

```text
PASS  the rule raised an alarm on coldroom-a1 and its command ended SUCCESSFUL
PASS  createCommand boost-cooling was accepted (QUEUED), not rejected
PASS  your own command reached SUCCESSFUL (the device answered; last status: SUCCESSFUL)
PASS  the operator sees both devices (2)
PASS  the operator sees alarms on both devices (2 devices with alarms)
PASS  the customer sees exactly coldroom-a1 (saw: coldroom-a1)
PASS  the customer sees alarms for coldroom-a1 only (saw: coldroom-a1)
PASS  the customer reads no state of coldroom-a2 (0 measurements)
PASS  the customer lists only the board shared with them (saw: tour-a-customer)
PASS  the operator's board is invisible to the customer
PASS  the read-only customer user cannot send a command (refused: FORBIDDEN)
```

A customer user who asks for a device that is not theirs gets an empty answer, not an error, so a
script cannot tell "not yours" from "does not exist". That is intended.

## The same tour for a second site {#second-site}

A site is a parameters file. [`sites/site-a.env`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/sites/site-a.env)
holds everything that differs between sites: the tenant, its administrator, the customer, the two
device tokens, and the numbers the rule and the simulated rooms use.

```bash
./tour.sh sites/site-b.env all
```

Site B is another tenant, `tour-b`, with another customer (`globex`, whose user is
`ops@globex.example`), other devices, and a lower alarm line (6 degrees rather than 8). Nothing but
the file differs. To make a site of your own, copy a site file, change the values, and run the same
command. Each tenant has its own administrator, customer user, device password and dashboards, and
neither can see the other.

## Optional chapters {#optional-chapters}

These are shorter, and each leads to the page that holds the detail. The **On by default** column
says whether a stock install already runs the feature, or whether you must turn something on first.

| Chapter | What it adds | Prerequisites | On by default? | Start here |
| --- | --- | --- | --- | --- |
| Enrollment and self-registration | Devices introduce themselves with a shared provisioning key, and make their own credential, instead of being created first. | A provisioning profile (**Connect → Provisioning Profiles**). Devices register over HTTPS through the instance's API ingress, so they need a route to it. | The capability is part of the standard install. Nothing registers until you create a provisioning profile. | [Device self-registration](../guides/device-self-registration.md) |
| X.509 client certificates | Devices authenticate to the broker with a certificate, either pinned to a device or trusted through a CA that registers devices on first connect. | `openssl` and a CA you trust (the guide mints a throwaway one). MQTT only; no CRL or OCSP. The repository's `examples/x509-device` runs the whole path. | Nothing to enable. Trust exists only once you add a trust anchor or pin a certificate. | [Connecting with a certificate](../guides/device-x509.md) |
| Two-factor authentication | A code from an authenticator app on password sign-ins, for one account or required for a tenant. | A person's authenticator app; correct clocks on every node. | Built in and **not required**: each person opts in under **Account security**, and a tenant administrator can require it. | [Two-factor authentication](../guides/two-factor-authentication.md) |
| Device configuration | A desired configuration, built from the profile's declared shared attributes, delivered to the device with revisions and an applied or rejected report. | Declare the keys on the profile and publish it. The device must be on MQTT; devices that speak only HTTP are not served. | Available on every install. A device receives nothing until the profile declares keys. | [Connecting a device](../guides/connecting-a-device.md#reserved-device-topics) |
| Signed over-the-air updates | An artifact catalogue, signers, signed assignments and rollouts, with each attempt tracked until the device proves the new version. | An object store for artifact content (a shared volume or S3), a registered signer, and an MQTT device that follows the update protocol. The reference device is `examples/ota-device`. | In the `default` profile. The `telemetry` and `ingest-only` profiles leave it out, and an instance without an object store refuses artifacts with `OBJECT_STORE_NOT_CONFIGURED`. | [Over-the-air updates](../concepts/over-the-air-updates.md) and [Signing updates](../guides/signing-updates.md) |
| SQL and BI | Read-only SQL over your own tenant's telemetry for Metabase, Grafana, Power BI or `psql`, through the `analytics` schema. | A reader that you declare. The platform's own database credentials are never handed to a BI tool. | The `analytics` schema exists on every install and has no readers until you declare one. | [SQL and BI access](../guides/sql-and-bi-access.md) |
| Custom widgets | Your own React widget, drawn on dashboards with the same data binding as the built-in ones. | Node and npm, and a rebuild of the console or `/dash` that includes your package. There is no runtime installer. | Off. Only the built-in widgets are present. | [Writing a custom widget](../guides/writing-a-custom-widget.md) and [Adding widget packages](../guides/adding-widget-packages.md) |

## If something did not work {#if-something-did-not-work}

| What you see | Usually means |
| --- | --- |
| `error: ... cannot sign in` from a script | A step before it did not run. Run `./01-provision.sh <site>` first; it creates the identity that the later scripts sign in as. |
| `could not read the broker CA` | `INSTANCE` is not your instance's name, or `kubectl` points at another cluster. Set `INSTANCE` and `KUBE_CONTEXT`. |
| The check waits and then fails on the first line | The devices are not publishing. Run `./02-devices.sh <site> logs`. `publish refused` means the credential or the tenant name is wrong; a connection error means `MQTT_HOST` is not your cluster node's container name. |
| A device is online but no alarm appears | The profile was not published, or the rule's line is above where the room stops warming. Check the site file's `TOUR_ALARM_ABOVE`. |
| A device log says `command ... answered` but the command stays `SENT` | The answer was not accepted. The answer must carry the command's own `token` and `dispatchNonce`. |
| The customer user sees no devices | The device is not assigned to the customer, or the member's scope was not set. `./04-customer-user.sh <site>` prints the scope it found. |

## Cleaning up {#cleaning-up}

```bash
./tour.sh sites/site-a.env down
./tour.sh sites/site-b.env down
```

stops the simulated devices. The tenants stay. Deleting a tenant revokes access at once, but its
token is held for at least 12 hours before it can be used again, and a tenant that still has
members is refused. See [Tenant deletion](../deployment/tenant-deletion.md). To run the tour again
from nothing straight away, destroy the instance with `dcctl destroy local <instance>` and bootstrap
a new one, or give the site file new tenant tokens.

## Where to go next {#where-to-go-next}

- **[Dashboards](/user-guide/dashboards)**: the canvas, widgets, versions and sharing.
- **[Detection rules](/user-guide/detection-rules)**: the form, the canvas and replaying history through a rule.
- **[Notification policies](/user-guide/notification-policies)**: telling a person when an alarm is raised.
- **[Commanding a fleet](../guides/commanding-a-fleet.md)**: one command to many devices.
