<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# rules-to-commands: a rule that acts, and a device that confirms

The whole automation loop in two pieces, with no code of yours in the middle:

1. A **furnace** (a few lines of shell) reports its temperature every 5 seconds. It heats 4 degrees a tick.
2. A **detection rule** on its device profile says: *above 80, raise a critical alarm and send the command `cool-down`.*
3. The furnace gets the command, switches its cooling on and **answers**. The answer is what turns the command from "sent" into `SUCCESSFUL`.
4. The temperature falls back below 80, the rule's condition stops holding, and the **alarm clears by itself**.

```
 furnace (Mosquitto container)                         DeviceChain (kind cluster)
┌────────────────────────────┐   temperature   ┌───────────────────────────────────────┐
│ heats 4 per tick           │────────────────▶│ detection rule  temperature > 80      │
│                            │                 │   ├─ raise alarm  (critical)          │
│ cools 8 per tick on command│◀── cool-down ───│   └─ send command cool-down           │
│ answers the command        │──── answer ────▶│ command: SUCCESSFUL   alarm: CLEARED  │
└────────────────────────────┘                 └───────────────────────────────────────┘
```

The rule is [`rule.json`](rule.json), exactly what the console's form saves, and the device is
[`device/furnace.sh`](device/furnace.sh). The device knows nothing about the rule: it reports and obeys.

## Requirements

`docker` (with `compose`), `kind`, `kubectl`, `jq`, `curl`, `openssl` and `dcctl`.

Run every script from this directory. They talk to `http://localhost` (the ingress of a default
`dcctl install local`). Set `DC_URL` if yours is elsewhere, `KUBECONFIG` or `KUBE_CONTEXT` to pick
the cluster, `INSTANCE` for the instance name (default `rules`) and `MQTT_HOST` for the kind node's
container name (default `devicechain-control-plane`, which is `<cluster>-control-plane`). Export the
same `MQTT_HOST` for `docker compose` and the scripts.

## Run it

```bash
# 1. DeviceChain.
dcctl install local --compact
dcctl bootstrap local rules --host localhost --no-tls --no-escrow

# 2. A tenant, the furnace profile with its rule, a device and its login.
./provision.sh

# 3. The furnace.
docker compose up -d
```

### What step 2 did

`provision.sh` created the tenant `rules-demo`; a device profile `furnace` that declares the metric
`temperature`, the command `cool-down` and the detection rule `furnace-overheat`; **published** the
profile (rules reach live telemetry only when the profile is published, and the platform's compiler
checks the rule at that moment); a device type and the device `furnace-1`; and an MQTT login for it.
The password is generated once into `.env` (git-ignored) and is never printed.

The rule:

```json
{
  "name": "Furnace over temperature",
  "type": "threshold",
  "severity": "critical",
  "when": { "metric": "temperature", "op": "gt", "threshold": 80 },
  "actions": [
    { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "furnace-overheat" } },
    { "type": "sendCommand",
      "sendCommand": { "command": "cool-down", "payload": "{\"targetC\":55}", "expiresAfter": "15m" } }
  ]
}
```

A rule is **edge-triggered**: the actions run when the condition starts to hold, and the alarm it
raised clears when the condition stops. Nothing authors the clear. `expiresAfter` bounds how long the
command may wait for a device that is offline.

## See it work

Give it a minute or two: the furnace starts at 60 and needs about six ticks to pass 80.

```bash
./show-state.sh
```

```
== furnace-1 ==
    temperature = 72 Cel
== alarms ==
    furnace-overheat  CRITICAL  CLEARED  raised 2026-10-30T12:32:58.710604Z  cleared 2026-10-30T12:33:18.801387Z  last value 84
== commands sent by the rule ==
    cool-down  SUCCESSFUL  outcome SUCCEEDED  queued 2026-10-30T12:33:08.965798Z  answered 2026-10-30T12:33:09.026518Z  "cooling to 55"
```

The furnace's side of the same cycle (`docker compose logs -t`):

```
12:32:53 temperature=80
12:32:58 temperature=84          <- alarm raised at this reading
12:33:03 temperature=88
12:33:08 temperature=92
12:33:08 command cool-down ...   <- the command arrives, and is answered 17 ms later
12:33:13 temperature=84
12:33:18 temperature=76          <- alarm cleared at this reading (the first one below 80)
12:33:33 target reached; cooling off
```

The furnace then heats again and the cycle repeats: one alarm and one command per excursion.
`docker compose logs -f furnace` shows it live, and the console shows it too: **Alarms** lists
the alarm, and the device's page shows the command history.

The command is `SUCCESSFUL` only because the furnace **answered** with the command's own token and the
delivery's `dispatchNonce` (see `furnace.sh`). A device that obeys but never answers leaves the
command `SENT` until it times out.

### Look at the rule in the console

Open the console (http://localhost/), **Devices → Device Profiles → Furnace → Detection Rules**.
The rule is listed and opens in the **Form**. In the drawer, switch to **Canvas** to see the same
rule as a flow, a Source wired to a Threshold wired to two Actions, and use **Preview against
history** to replay the furnace's recorded telemetry through the rule without raising anything. The
form and the canvas edit the same rule, and the same compiler checks both.

## Results (kind, DeviceChain v0.27.0, Mosquitto 2.0.22)

| Check | Result |
| --- | --- |
| Profile with the rule publishes | yes; the compiler accepted it |
| Alarm raised when the reading passes 80 | yes, `CRITICAL`, at the 84 reading |
| Command sent by the rule | queued together with the alarm, within about a second of the reading that fired the rule |
| Device answers | `SUCCESSFUL`, outcome `SUCCEEDED`, answer payload stored |
| Alarm clears without being authored | yes, at the first reading at or below 80 |
| Second excursion | a second alarm and a second command, same behaviour |

## Things worth knowing

1. **Actions are independent.** The alarm and the command are separate actions of the same firing; both
   are published together, normally within about a second of the reading (a static threshold like this
   one does not wait for the engine's checkpoint). See
   [how quickly a detection reaches its actions](https://docs.devicechain.io/deployment/detection-engine#how-quickly-does-a-detection-reach-its-actions).
2. **A profile that declares commands accepts only those.** The `cool-down` definition is what the
   rule's `sendCommand` action names; `provision.sh` creates it before the rule.
3. **Publish, then rules run.** Editing the draft changes nothing live. Publishing atomically replaces
   the profile's rules; roll back from the profile's **Versions** tab.
4. **The command payload is a JSON string** (`"{\"targetC\":55}"`) in the rule, and an object in the
   delivery the device receives.

## For a real fleet

- Add a **notification policy** so a person is told: the alarm only notifies when a channel is set up
  for its severity. See the notification guide.
- Give the command a short `expiresAfter` that matches how long the action is still useful.
- Guard actions on the canvas (for example, only send the command in a particular band).

## Clean up

```bash
docker compose down
rm -rf .env pki
dcctl destroy local rules
```

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | Run as the published image, pinned by digest; `mosquitto_pub` and `mosquitto_sub` are the furnace's MQTT client. |
| [jq](https://jqlang.github.io/jq/), [curl](https://curl.se/), [OpenSSL](https://www.openssl.org/) | MIT / curl / Apache-2.0 | Run on the host by the scripts. |

`furnace.sh` and the scripts are licensed with the rest of this repository (Apache-2.0).
