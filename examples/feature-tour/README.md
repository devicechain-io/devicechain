<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# feature-tour: one site, end to end, then the same tour again

A scripted tour of the core path of the platform, with one coherent site:

device type and profile → devices → readings → dashboard → rule → alarm → command outcome →
a **customer user who sees only their own device**.

It then runs the same tour as a **second tenant** with a different customer, by changing one
parameters file. Nothing is forked.

The walkthrough, with the console steps beside each script step and the optional chapters
(self-registration, X.509, two-factor, configuration, signed updates, BI, custom widgets), is the
[Feature Tour](https://docs.devicechain.io/quickstart/feature-tour) page of the documentation. This
file is the reference for the scripts.

It uses only supported interfaces: `dcctl` to bring up the instance, the public GraphQL API for
everything else, and the console to look at the result. DeviceChain has no import for a whole site;
these scripts are ordinary API calls.

## Requirements

`bash` (Git Bash on Windows), `curl`, `jq`, `openssl`, `docker` and `kubectl`, and a running instance:

```bash
dcctl install local --compact
dcctl bootstrap local tour --host localhost --no-tls --no-escrow
```

The simulated devices are containers on the `kind` Docker network, which reach the broker's MQTT node
port by the cluster node's container name.

## Run it

```bash
./tour.sh sites/site-a.env all      # everything, then the checks
./tour.sh sites/site-b.env all      # the same tour, as another tenant and customer
```

Or by step, each taking the site file as its first argument:

| Script | Does |
| --- | --- |
| `01-provision.sh` | the tenant and its administrator; the `cold-room` profile (measurements, a command, one rule), published; the device type; two devices with MQTT credentials; a customer; the assignment of device 1 to it |
| `03-dashboards.sh` | two boards from `dashboard.jq`, an operator's (with a command button) and a customer's (without); both published, the customer's shared with the customer |
| `04-customer-user.sh` | the customer user: a read-only `viewer` limited to one customer |
| `02-devices.sh <site> up\|down\|logs` | the two simulated cold rooms (`device/coldroom.sh` in a Mosquitto container each) |
| `05-verify.sh` | waits for the rule to raise an alarm and send a command, sends one of its own, then checks what the operator sees and what the customer sees (`PASS`/`FAIL`) |

Every script is safe to run again.

## Where your instance is

Environment variables override the site file; each has a default that matches the commands above.

| Variable | Default | |
| --- | --- | --- |
| `DC_URL` | `http://localhost` | the console's address |
| `INSTANCE` | `tour` | the dcctl instance (namespace `dci-<instance>`) |
| `MQTT_HOST` | `devicechain-control-plane` | the kind node's container name, `<cluster>-control-plane` |
| `MQTT_PORT` | `31883` | the broker's node port |
| `KUBE_CONTEXT` | the current context | which cluster `kubectl` talks to |
| `DC_ADMIN_PASSWORD` | read from Secret `dci-<instance>-superuser` | the superuser's password |

## A site is a parameters file

`sites/site-a.env` and `sites/site-b.env` hold everything that differs: the tenant and its name, the
administrator's email, the customer, the two device tokens, and the numbers the rule and the
simulated rooms use (`TOUR_ALARM_ABOVE`, `TOUR_START_C`, `TOUR_TARGET_C`, `TOUR_TICK_SECONDS`). To
make a site of your own, copy one and change the values.

Passwords the scripts generate are written to `.state/<tenant>/` (git-ignored, mode 0600) and are
never printed. The broker's CA is written to `pki/` (git-ignored).

## Clean up

```bash
./tour.sh sites/site-a.env down       # stops the devices
./tour.sh sites/site-b.env down
dcctl destroy local tour              # removes the instance, and with it both tenants
rm -rf .state pki
```

A deleted tenant's token is held for at least 12 hours, and a tenant that still has members cannot
be deleted, so to run the tour again from nothing straight away, destroy and bootstrap the instance,
or use new tenant tokens in the site file.

## Evidence

[`evidence/`](evidence) holds the output of a clean run of each site on a fresh kind cluster
(DeviceChain v0.27.0) and console screenshots of the operator's and the customer user's views.

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | Run as the published image, pinned by digest; `mosquitto_pub` and `mosquitto_sub` are the cold rooms' MQTT client. |
| [jq](https://jqlang.github.io/jq/), [curl](https://curl.se/), [OpenSSL](https://www.openssl.org/) | MIT / curl / Apache-2.0 | Run on the host by the scripts. |

The scripts are licensed with the rest of this repository (Apache-2.0).
