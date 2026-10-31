<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# migration-pilot: a worked evaluation of moving a fleet to DeviceChain

This example answers one question for a team that runs devices on another IoT platform: **what would
it take, concretely, to move them here, and can I try it without risking the fleet?** It follows one
representative shape end to end, with real output kept in [`evidence/`](evidence/):

```
 source/ (a sanitized export          mapping.json            DeviceChain
 of "the previous platform")          (concept -> concept)    (public GraphQL only)
┌──────────────────────────┐        ┌────────────────┐      ┌──────────────────────────────┐
│ 25 devices, 2 types      │ plan   │ type -> profile│      │ profiles, metrics, rules      │
│ telemetry keys           │───────▶│ key  -> metric │─────▶│ device types                  │
│ 3 rules, 1 dashboard     │        │ rule -> rule   │ model│ 23 devices + their access     │
│ an access token each     │        └────────────────┘ check│ tokens (dry run first)        │
└──────────────────────────┘                           import└──────────────┬───────────────┘
                                                                            │ reconcile
 pilot (3 simulated devices) ── publish to BOTH ──────────────────────────▶ │ compare
 one config line flips them back; DeviceChain can also shut them out        ▼
```

**What this is not.** It is a feasibility path. It is not a converter: DeviceChain does not read another
platform's export files, and nothing here pretends it does. There is no solution-bundle import, no
rule converter, no dashboard importer and no history converter. The export in `source/` is a
made-up file shape that stands in for whatever your platform lets you extract; the scripts parse
it, and the part you would rewrite for a real fleet is the few lines that read it. Everything the scripts
send to DeviceChain goes through the public GraphQL API (and, for the pilot, MQTT), so the same calls
are available to your own tooling.

## Requirements

`docker` (with `compose`), `kind`, `kubectl`, `helm`, `jq`, `curl` and `dcctl`. No other tools. The
scripts are bash and run from Git Bash on Windows as well as on Linux and macOS.

## Run it

```bash
# 1. An instance (a small one; see the quickstart for what these do)
dcctl install local --compact --no-tls
dcctl bootstrap local pilot --host localhost --no-tls --no-escrow

# 2. Look at the plan. This reads files only and touches nothing.
./migrate.sh plan

# 3. Build the model, preview the devices (a dry run), then import them.
./migrate.sh model
./migrate.sh check
./migrate.sh import

# 4. Prove nothing was lost.
./reconcile.sh

# 5. Try three devices side by side, then roll them back.
./pilot.sh up
./pilot.sh compare
./pilot.sh rollback --disable
```

Set `DC_URL` if the ingress is not at `http://localhost`, `KUBECONFIG` (or `KUBE_CONTEXT`) to pick the
cluster, `INSTANCE` for the dcctl instance name (default `pilot`), and `KIND_CLUSTER` for the kind
cluster's name, which names the node the simulated devices connect to (default `devicechain`).
Scripts read the superuser's password from the cluster, as `dcctl bootstrap` stored it, and never
print it. Everything the scripts produce goes to `out/` (git-ignored; it holds the access tokens
and is mode 0600 where it matters).

## 1. The source fleet

[`source/`](source/) is invented and sanitized. Every name and every token is made up (the tokens
read `EXAMPLE-TOKEN-...`), and no real platform's file format is implied.

| File | What it is |
| --- | --- |
| `devices.csv` | 25 devices: a name, a serial number, a type, a site, a firmware version, and a credential (an access token for 24 of them, a short password for one) |
| `device-types.json` | 2 types, with 8 telemetry keys between them (one a text key) |
| `rules.json` | 2 threshold rules, and 1 rule that is a chain of script and REST nodes |
| `dashboard.json` | a description of one dashboard: 4 widgets and a selector |

Two of the 25 devices are deliberately wrong, because a real export always has some: `PC-0007` has a
7-character password, and the last row repeats the serial number `CC-0003`.

## 2. The model mapping

[`mapping.json`](mapping.json) is the one place the mapping is written down. The scripts do not hard-code it.

| In the source | In DeviceChain | Carried? |
| --- | --- | --- |
| device type | a **device profile** (the versioned contract) and a **device type** that adopts it | yes |
| telemetry key, `double` / `long` / `boolean` | a **metric** on the profile: `DOUBLE` / `INT` / `BOOLEAN`, with its unit | yes |
| telemetry key, `string` | nothing: a metric must be numeric or boolean | **no**: report the value as an attribute, or drop it |
| device name | `name`; the serial number becomes the device **token** (lower case) and the `externalId` | yes |
| site, firmware, old name | the `metadata` on the device; `site` also a `SERVER` attribute, which is what a dashboard or group selects on | yes |
| `ACCESS_TOKEN` credential | an `ACCESS_TOKEN` credential, imported with the device | yes |
| `BASIC` (user and password) credential | an `MQTT_BASIC` credential, **if the password is at least 16 bytes** | only if long enough |
| threshold rule | a detection rule on the profile: `threshold`, or `duration` when the source holds the condition for some minutes | yes |
| script-chain rule | nothing | **no**: rebuilt by hand |
| dashboard | nothing | **no**: rebuilt by hand in the canvas editor |
| stored history | nothing | **no**: see [History](#history) |

## 3. The preview

`./migrate.sh plan` prints what would happen, from the files alone ([`evidence/01-plan.txt`](evidence/01-plan.txt)):

```
Cold Chain Sensor  ->  profile cold-chain-sensor, device type cold-chain-sensor
    metric temperature  DOUBLE  Cel
    metric battery  INT  %
    SKIPPED status_text (string): a metric must be numeric or boolean; report the value as a device attribute instead
...
Pump pressure check and restart  ->  SKIPPED: not carried: a free-form script flow has no equivalent; rebuild it as rules plus a webhook
Cold chain overview  ->  SKIPPED: dashboards are rebuilt by hand in the canvas editor (4 widgets to place)
```

Once the model exists, `./migrate.sh check` is the server's own dry run of the device import. It decides every
row by the same code as the real import and writes nothing ([`evidence/03-dry-run.txt`](evidence/03-dry-run.txt)):

```
dryRun=true  created=23 exists=0 conflict=0 invalid=2 refused=0
  row 23 pc-0007: INVALID INVALID_VALUE: credentialValue must be at least 16 bytes for an MQTT_BASIC credential
  row 25 cc-0003: INVALID DUPLICATE_IN_FILE: token also appears at row 3
```

## 4. The import

`./migrate.sh model` and `./migrate.sh import` use these public GraphQL mutations and nothing else:
`createTenant` (instance administration; the only call on the admin plane), `createDeviceProfile`,
`createMetricDefinition`, `createDetectionRule`, `publishDeviceProfile`, `createDeviceType`,
`importDevices` (in `FACTORY_CREDENTIALS` mode) and `setEntityAttribute`.

Both are safe to run again. A second `import` answers `created=0 exists=23` and changes nothing
([`evidence/06-import-rerun.txt`](evidence/06-import-rerun.txt)). `dcctl` is not used for the data: it stands up the instance
and does not import entities.

## 5. The reconciliation report

`./reconcile.sh` makes two independent checks and exits non-zero unless both agree
([`evidence/05-reconciliation.md`](evidence/05-reconciliation.md), the first run on an empty tenant):

| Item | In export | Created | Already there | Skipped | Accounted | Match |
| --- | ---: | ---: | ---: | ---: | ---: | :---: |
| profile | 2 | 2 | 0 | 0 | 2 | yes |
| deviceType | 2 | 2 | 0 | 0 | 2 | yes |
| metric | 8 | 7 | 0 | 1 | 8 | yes |
| rule | 3 | 2 | 0 | 1 | 3 | yes |
| dashboard | 1 | 0 | 0 | 1 | 1 | yes |
| device | 25 | 23 | 0 | 2 | 25 | yes |
| credential | 25 | 23 | 0 | 2 | 25 | yes |
| attribute | 25 | 23 | 0 | 2 | 25 | yes |

1. **Accounting.** Every item in the export is created, already there, or skipped with a reason, so the three
   must add up to what the export held.
2. **Read back.** The totals are then counted by asking DeviceChain (metrics, rules, devices, credentials and
   attributes), and must equal what the ledger says was created.

Both agreed: 23 devices, 23 credentials, 7 metrics and 2 rules were counted in the cluster. The nine skipped lines each carry a
reason, in the same file.

A check is worth nothing until it has been seen to fail. [`evidence/09-negative-control.txt`](evidence/09-negative-control.txt)
deletes one migrated device behind the report's back: section 2 turns to `NO` for devices, credentials and
attributes (23 against 22), and `./migrate.sh import` then puts it back.

## 6. What you need to know before you commit

### Credentials: what can be reused

- An **access token** is imported as it is and keeps working. The device presents it as before, so it needs no
  new secret. The platform stores only a keyed digest of it and never shows it again, so your export file is the
  only copy: protect it and delete it when the import is done.
- A **user and password** credential can be reused only if the password is **at least 16 bytes**. A shorter one is
  refused with a reason (`PC-0007` above), and the device needs a new password. There is no way to relax the rule.
- A **client certificate** is not an import at all. DeviceChain checks it against a trust anchor your tenant adds;
  see [Connecting with a certificate](../../docs/docs/guides/device-x509.md).
- The device's identity is its **token**, which you choose. Keep the old serial number as the token, as here, and
  as the `externalId`, so people and reports can still find it.

### Rules and dashboards you must rebuild

- A **threshold rule**, and a threshold held for some minutes, map directly to a detection rule. The scripts write
  the two in this example. Check each one by eye, because the mapping from your platform's operators and severities is
  the part most likely to need editing in `mapping.json`.
- A **flow of script nodes, REST calls and branches** has no equivalent. Detection rules are conditions over events
  that raise an alarm, send a command, call a webhook or publish to a connector. A chain like the pump rule in
  this example becomes one or more rules plus a webhook to a service you run. It is rebuilt by hand, here and in
  any fleet.
- A **dashboard** is rebuilt in the canvas editor. DeviceChain imports its own dashboards only. The description in
  `dashboard.json` is what you would work from: the widgets, their metrics, the 24-hour range, and the selector,
  which becomes a *slot* bound to a device or customer when the board is opened.

### What the devices must change

None of this is avoidable, and a device that cannot be changed needs a bridge in front of it. This is a feasibility
path, **not** a no-reflash converter.

| | Previous platform | DeviceChain |
| --- | --- | --- |
| Broker and TLS | its host, often plain | the instance's MQTT listener (TLS), checked against the instance CA |
| Username | the token | `{tenant}:{token}` for an access token, no password |
| MQTT client id | anything | `{instanceId}:{tenant}:{deviceToken}`, optionally with a `:suffix`; any other is refused |
| Topic | its own, here `legacy/telemetry/<name>` | `{instanceId}/{tenant}/devices/{token}/events` |
| Payload | a flat object, `{"temperature":5.5,"door_open":true}` | an envelope with a `device`, an `eventType` and `payload.entries[].measurements`, each reading a string |
| Text readings | allowed | not a metric; see the findings below |

[`pilot/device.sh`](pilot/device.sh) shows both payloads in a short script, and is the shape of the change your firmware (or a
bridge) needs. Set `altId` and `occurredTime` on every event, so that a retry stores one copy.

### History

**History conversion is not supported.** There is no importer for another platform's stored readings, and this
example does not attempt one. The ingest path accepts readings with their own `occurredTime`, up to 366 days old,
so you can replay history you have extracted yourself, set an `altId` on each, and pace it under the tenant's ingest
limit. Whether you need it at all is the first question: keeping the old store read-only for reporting, and
starting the new one at the cutover, is often the cheaper answer.

## 7. The side-by-side pilot

`./pilot.sh up` starts three simulated devices (two cold-chain sensors and a pump controller) in containers on
the cluster's network. A fourth container is a stand-in for the previous platform's broker, and a fifth records
everything it receives. Every three seconds each device takes a reading and publishes it **twice**: flat to the
stand-in, and as the DeviceChain envelope to the cluster. The readings are a pure function of the tick number, so
both platforms should hold the same values.

`./pilot.sh compare` checks that after a minute and a half ([`evidence/07-pilot-side-by-side.txt`](evidence/07-pilot-side-by-side.txt)):

```
device   key            legacy deviceChain  result
cc-0001  temperature        33       33  match
cc-0001  humidity           33       33  match
cc-0001  status_text        35        0  not sent: a text value cannot be a metric
cc-0002  battery            36       35  match
pc-0001  flow_rate          37       37  match
```

(The counts differ by one or two at most, because the previous platform's file is read just after DeviceChain is. The report
accepts DeviceChain's values only if they are the other platform's, in order, from the start.) The pilot also showed
the migrated `Battery low` rule working: a sensor reporting 12 raised a `WARNING` alarm. One caveat is in the findings below.

### The way back

The cutover is reversible in two independent steps, and the previous platform is never touched at any point:

1. **Flip the target.** The device reads `pilot/state/targets` on every tick. `legacy dc` publishes to both,
   and `legacy` publishes to the previous platform only. In a real fleet this is one configuration value on the
   device or its bridge. It needs no restart and no reflash, and it is the first thing to do.
2. **Shut it out in DeviceChain**, if you want a hard stop: `./pilot.sh rollback --disable` disables the
   pilot devices (or `--delete` removes them). A disabled device cannot open a connection (the broker refuses it
   like a wrong password) and what it sends is discarded. `./pilot.sh resume` re-enables them and publishes to
   both again.

[`evidence/08-rollback.txt`](evidence/08-rollback.txt) shows it. With the devices still publishing and
`./pilot.sh quarantine` applied, the device logs `Connection Refused: not authorised` and DeviceChain's reading count stops
moving while the previous platform's keeps growing:

```
device   enabled   legacy readings   DeviceChain readings
cc-0001  false                  42                    140
cc-0001  false                  51                    140      <- 20 seconds later
...
cc-0001  true                   75                    184      <- after ./pilot.sh resume
```

(DeviceChain counts one row per key, so it counts several for every message the other counts once.) The old platform kept
its full record throughout (the legacy column never paused). The window the devices were disabled or flipped
is a gap in DeviceChain's record, which is the price of rolling back, and the reason to pilot with devices whose
readings you can afford to lose.

## 8. Findings worth knowing

Things the run turned up. They are properties of the product today, and each is the kind of thing a
migration plan should budget for.

1. **A text reading is dropped quietly.** A device that sends `"status_text":"ok"` in `measurements` gets a
   normal acknowledgement, and the other readings in the same message are stored, but the text is dropped. The
   only trace is a warning in the device-management log (`Dropping unstorable measurement`). Check every text key of the old
   payload and decide, per key, where it goes.
2. **A key the profile does not declare is stored anyway**, without a unit or type (`undeclared_key` was stored
   as 5). Declaring every key on the profile first is what gives a reading its unit and lets a rule see it.
3. **A boolean sent as `"true"` is accepted** and stored as 1, as is `"1"`. The pilot sends `"0"`/`"1"`.
4. **Deleting a device does not delete its readings.** After `rollback --delete`, the readings were still there
   (188 rows for `cc-0001`), and a device recreated with the same token owns them again. That is useful (a
   rollback does not destroy evidence), and `pilot.sh` therefore counts only from the start of the run. It is also
   why a pilot you want to start clean needs new tokens.
5. **A rule that fires on a change does not fire for a condition that already holds.** `cc-0002` reports a
   battery of 12 from its first message. The first run raised `Battery low` at once. After the device was deleted
   and recreated, the same stream raised nothing for over a minute, until a reading above the limit (90) and then one
   below (12) was sent by hand. If you recreate pilot devices, expect a device already past a limit to stay quiet
   until it crosses it again. This was observed, and the cause has not been isolated further here.
6. **A password shorter than 16 bytes cannot be carried** (above), and **a repeated serial number is caught in
   the file** before anything is written (`DUPLICATE_IN_FILE`), which is exactly what the dry run is for.
7. **No bulk path for attributes or rules.** Devices import in bulk (1,000 rows a call). Everything else here is
   one call per item. For 25 devices that is nothing; for a large fleet it is a script you loop.

## Clean up

```bash
./pilot.sh down
rm -rf out pilot/.env pilot/pki pilot/state pilot/logs
dcctl destroy local pilot
```

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | Run as the published image, pinned by digest: the stand-in broker, its recorder, and each simulated device's MQTT client. |
| [jq](https://jqlang.github.io/jq/), [curl](https://curl.se/) | MIT / curl | Run on the host by the scripts. |

The scripts and the example data are licensed with the rest of this repository (Apache-2.0).
