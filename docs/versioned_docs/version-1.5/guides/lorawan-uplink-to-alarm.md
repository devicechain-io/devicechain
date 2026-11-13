---
sidebar_position: 3.9
title: LoRaWAN Uplink to Alarm
---

# LoRaWAN Uplink to Alarm {#lorawan-uplink-to-alarm}

By the end of this page, a binary LoRaWAN uplink has crossed a network server's integration, been turned into readings by a decoder you wrote and published, tripped a detection rule, and raised an alarm that clears by itself. In the last step, the rule also sends the sensor a command, and the command waits for the device to acknowledge it. You need no gateway and no sensor: the simulator plays the network server and posts the requests a real ChirpStack or The Things Stack posts. The commands are the ones you would use with real hardware, and [the last section](#real-network-server) says what changes.

Budget about forty-five minutes, most of it the install.

:::note This page needs v1.2
Decoding uplinks with a decoder you publish arrived in v1.2, together with the `dc-simulator` flags used here (`--lorawan-binary` and `--lorawan-print-decoder`). On v1.1 or earlier, an uplink that carries no decoded object is acknowledged and stores nothing, so this page does not work from [step 3](#3-the-decoder) on. Check the instance's version before you start.
:::

The path the uplink takes:

```text
 dc-simulator                         DeviceChain
┌──────────────────────┐  HTTPS POST  ┌─────────────────────────────────────────────────────────────┐
│ plays ChirpStack or  │─────────────▶│ inbound integration ──▶ decoder on the device profile       │
│ The Things Stack     │  raw frame,  │   finds the device        six bytes ──▶ temperature,        │
│                      │  no decoded  │   by its DevEUI           humidity, battery.voltage, door   │
└──────────────────────┘  object      │                                   │                         │
                                      │                                   ▼                         │
                                      │ alarm room-too-warm ◀── detection rule  temperature > 25   │
                                      └─────────────────────────────────────────────────────────────┘
```

A network server with a codec or a payload formatter hands DeviceChain an object it already decoded, and the integration stores its numbers as they are. Here the network server has no codec for the sensor, so it forwards the raw bytes and the decoder on the device profile reads them. The byte layout of the device then lives in one place that you version, test and roll back with the rest of the profile.

## What you need {#requirements}

- A running instance of v1.2 or later on a laptop, installed as in [Your first device](../quickstart/first-device.md): `dcctl install local --compact`, then `dcctl bootstrap local devicechain --host localhost --no-tls --no-escrow`. The instance is named `devicechain`, which is what `dcctl` expects when you leave out `--instance`.
- `curl` and `jq`.
- `dc-simulator`, attached to every release from v1.2 beside `dcctl`. Put it on your `PATH`, and check it with `dc-simulator --lorawan-print-decoder`, which prints a JSON document.

The commands call the instance at `http://localhost`. If your console is on another port, set the address once, and give `dcctl sim create` the same host and port with `--server` (for example `--server localhost:8080`), and the instance's name with `--instance` if you did not call it `devicechain`:

```bash
export DC_URL=http://localhost
```

All of it is also in [`examples/lorawan-uplink-to-alarm`](https://github.com/devicechain-io/devicechain/tree/main/examples/lorawan-uplink-to-alarm) as scripts, and its `run.sh` does the whole walkthrough in one command.

## 1. Get a tenant token {#1-get-a-tenant-token}

`dcctl sim create` makes a tenant and an identity scoped to it, and writes the identity's password into a handshake file. Use it to get a tenant to work in:

```bash
dcctl sim create lora
```

The tenant is `sim-lora`, and the identity is `lora@sim.devicechain.local`.

Authentication takes two calls: a login that returns an identity token, then an exchange of that token for one scoped to the tenant. The helper below sends a GraphQL request to one of the instance's services. Define it once, because every later step uses it, and then get the token:

```bash
gql() { # SERVICE TOKEN QUERY [VARIABLES-JSON]
  local v=${4:-'{}'}
  curl -sS -X POST "$DC_URL/api/$1/graphql" \
    -H 'Content-Type: application/json' ${2:+-H "Authorization: Bearer $2"} \
    -d "$(jq -nc --arg q "$3" --argjson v "$v" '{query:$q,variables:$v}')" | jq -c .
}

PW=$(jq -r .simPassword ~/.devicechain/sims/lora.json)
IDT=$(gql user-management '' 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
  "$(jq -nc --arg p "$PW" '{e:"lora@sim.devicechain.local",p:$p}')" | jq -r .data.login.identityToken)
export DC_TOKEN=$(gql user-management '' 'mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}' \
  "$(jq -nc --arg t "$IDT" '{t:$t,n:"sim-lora"}')" | jq -r .data.selectTenant.accessToken)
```

The token lasts 15 minutes. When a later call fails with an authentication error, run the last four lines again.

## 2. Declare what the sensor measures {#2-the-profile}

A **device type** points at a **device profile**, and the profile declares the measurements, their units, the decoder and the rules. Create the profile and one metric for each reading the sensor sends:

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceProfile(request:{token:"room-sensor",name:"Room sensor",category:"sensor"}){token}}'

metric() { # KEY TYPE UNIT
  gql device-management "$DC_TOKEN" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){metricKey}}' \
    "$(jq -nc --arg k "$1" --arg t "$2" --arg u "$3" \
      '{r:({token:("room-sensor-"+($k|gsub("[.]";"-"))),deviceProfileToken:"room-sensor",metricKey:$k,name:$k,dataType:$t} + (if $u=="" then {} else {unit:$u} end))}')"
}
metric temperature DOUBLE Cel
metric humidity DOUBLE %
metric battery.voltage DOUBLE V
metric door BOOLEAN ''
```

```text
{"data":{"createDeviceProfile":{"token":"room-sensor"}}}
{"data":{"createMetricDefinition":{"metricKey":"temperature"}}}
{"data":{"createMetricDefinition":{"metricKey":"humidity"}}}
{"data":{"createMetricDefinition":{"metricKey":"battery.voltage"}}}
{"data":{"createMetricDefinition":{"metricKey":"door"}}}
```

A decoder may write only to metrics the profile declares as measured, so they come first. The `Cel` and `V` units are UCUM codes.

## 3. The decoder {#3-the-decoder}

The simulated sensor sends six bytes on LoRaWAN port 2:

| Byte | Meaning |
| --- | --- |
| 0 | Layout version, always `1` |
| 1 to 2 | Temperature in tenths of a degree, signed, big-endian |
| 3 | Humidity in percent |
| 4 | Battery voltage in tenths of a volt |
| 5 | Bit 0 is the door contact |

The simulator prints the decoder document that reads this layout. Keep it in a file:

```bash
dc-simulator --lorawan-print-decoder | tee decoder.json
```

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "telemetry",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "temperature", "expr": "double(i16be(frame, 1)) / 10.0"},
        {"key": "humidity", "expr": "double(u8(frame, 3))"},
        {"key": "battery.voltage", "expr": "double(u8(frame, 4)) / 10.0"},
        {"key": "door", "expr": "bit(frame, 5, 0)"}
      ]
    }
  ],
  "vectors": [
    {
      "name": "door open",
      "input": {"frameHex": "0100fa2d1f01", "fPort": 2},
      "expect": {"temperature": 25, "humidity": 45, "battery.voltage": 3.1, "door": 1}
    }
  ]
}
```

Read it from the top:

- `"input": "FRAME"` makes the decoder read the raw bytes of the uplink, as the name `frame`.
- A **section** applies when its `when` condition holds. This one wants port 2 and a first byte of `1`, so a frame in a layout version you have not written a section for is refused, not misread. The first section whose condition holds is the one that runs.
- Each **output** names a metric and the expression that computes its value. `i16be(frame, 1)` reads a signed 16-bit big-endian number at byte 1, and `bit(frame, 5, 0)` reads the lowest bit of byte 5.
- A **vector** is a test case that travels with the decoder: this frame must produce exactly these readings.

The [payload decoders](./payload-decoders.md) guide describes every part. Before you save the decoder, run it. `testPayloadDecoder` evaluates the document over a frame with the platform's own evaluator and stores nothing:

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok section readings{key value} vectors{name ok}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d1f01",fPort:2}}')" \
  | jq '.data.testPayloadDecoder | {ok, section, readings: (.readings | map({(.key): .value}) | add), vectors}'
```

```json
{
  "ok": true,
  "section": "telemetry",
  "readings": {
    "temperature": "25",
    "humidity": "45",
    "battery.voltage": "3.1",
    "door": "1"
  },
  "vectors": [
    {
      "name": "door open",
      "ok": true
    }
  ]
}
```

Frame `0100fa2d1f01` is version 1, `00fa` = 250 tenths = 25 degrees, 45 percent humidity, `1f` = 31 tenths = 3.1 volts, and the door bit set. Now cut the frame short, to see what a refusal looks like. This is the same call with a four-byte frame and a different part of the result:

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok error{reason path message offset}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d",fPort:2}}')" \
  | jq -c .data.testPayloadDecoder
```

```json
{"ok":false,"error":{"reason":"eval_error","path":"sections[0].outputs[2].expr","message":"u8: reads 1 byte(s) at offset 4 of a 4-byte frame (offset 4)","offset":4}}
```

The reason names the failing expression and the offset the read went past. A live uplink that fails this way is refused whole, and [step 10](#10-when-an-uplink-does-not-decode) shows where that is recorded.

Save the decoder on the profile's draft:

```bash
gql device-management "$DC_TOKEN" 'mutation($t:String!,$d:String!){setDeviceProfileDecoder(profileToken:$t,document:$d){token}}' \
  "$(jq -nc --rawfile d decoder.json '{t:"room-sensor",d:$d}')"
```

```text
{"data":{"setDeviceProfileDecoder":{"token":"room-sensor"}}}
```

In the console, the same document goes in **Devices → Device Profiles →** your profile **→ Decoder**, where the field table writes these expressions for you.

## 4. The rule, and publishing {#4-the-rule}

A **detection rule** watches the readings of a profile and acts when a condition starts to hold. This one raises a warning alarm when `temperature` is above 25. Save it as `rule.json`:

```json
{
  "name": "Room too warm",
  "description": "Above 25 degrees: raise a warning alarm. The alarm clears by itself when the reading drops back to 25 or below.",
  "type": "threshold",
  "severity": "warning",
  "when": { "metric": "temperature", "op": "gt", "threshold": 25 },
  "actions": [
    { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "room-too-warm" } }
  ]
}
```

A rule is edge-triggered: the alarm is raised when the condition starts to hold and clears when it stops, and nothing authors the clear. The rule reads the readings the decoder produced, not the bytes, so it is the same rule whichever sender or decoder those readings came from. The rule goes on the profile as a JSON document in a string:

```bash
gql device-management "$DC_TOKEN" 'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
  "$(jq -nc --rawfile d rule.json '{r:{token:"room-too-warm",deviceProfileToken:"room-sensor",name:"Room too warm",enabled:true,definition:$d}}')"
```

```text
{"data":{"createDetectionRule":{"token":"room-too-warm"}}}
```

The decoder and the rule are still on the draft. Neither reaches a live uplink until you **publish** the profile, which freezes the draft as a version:

```bash
gql device-management "$DC_TOKEN" 'mutation{publishDeviceProfile(token:"room-sensor",label:"decoder and rule"){version}}'
```

```text
{"data":{"publishDeviceProfile":{"version":1}}}
```

Publishing is where the platform checks the profile. It compiles the decoder against the metrics being published, runs the decoder's saved vectors, and compiles the rule. It refuses the publish, naming the first expression or vector that fails, so a decoder whose own test case does not pass never goes live.

## 5. A device, known by its DevEUI {#5-a-device}

A LoRaWAN network server names a device by its DevEUI. DeviceChain finds the device that carries the same value as an **alias**. Create a device type for the profile, and a device with the alias:

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceType(request:{token:"room-sensor",name:"Room sensor",profileToken:"room-sensor"}){token}}'

gql device-management "$DC_TOKEN" 'mutation{createDevice(request:{token:"room-4",name:"Room 4",deviceTypeToken:"room-sensor",aliases:[{scheme:"lorawan.deveui",value:"70B3D57ED0050004"}]}){token aliases{scheme value}}}'
```

```text
{"data":{"createDeviceType":{"token":"room-sensor"}}}
{"data":{"createDevice":{"token":"room-4","aliases":[{"scheme":"lorawan.deveui","value":"70B3D57ED0050004"}]}}}
```

The DevEUI `70B3D57ED0050004` is one whose simulated temperature climbs from 22.4 to 26.9 degrees over ten uplinks and then starts again. That is what makes the alarm rise and clear within a minute.

## 6. The integration {#6-the-integration}

An **inbound integration** is the address a network server posts to. It is scoped to device types, so whatever posts to it can reach devices of those types and no others. Create one for ChirpStack v4, and keep the bearer token it returns:

```bash
export INTEGRATION_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"chirpstack",name:"ChirpStack",type:"CHIRPSTACK_V4",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token type identity} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)
```

The token is shown once and stored only as a keyed digest. This command keeps it in a shell variable; save it somewhere safe if you will need it again. If you lose it, `rotateIntegrationToken` issues another. `linkMetrics:true` stores the signal strength, signal-to-noise ratio and gateway count of each uplink beside its readings.

The integration is now reachable at `$DC_URL/api/integrations/sim-lora/chirpstack`: the tenant, then the integration's token. See [the URL](./inbound-integrations.md#url).

## 7. Send uplinks {#7-send-uplinks}

Run the simulator as a ChirpStack with no codec for the sensor: twelve uplinks, two seconds apart.

```bash
DC_SIM_INTEGRATION_TOKEN="$INTEGRATION_TOKEN" dc-simulator \
  --lorawan-ns chirpstack --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/chirpstack" \
  --deveui 70B3D57ED0050004 --uplinks 12 --emit-interval 2s
```

```json
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"runId":"1793997768570354300"}
```

`--lorawan-binary` is what makes this a network server with no codec: each request carries only the raw frame (`data`, in base64) and the port, with no decoded object. The token goes in through the environment so that it stays out of the process list. The summary line is the platform's answer, and all twelve were `202 Accepted`. If your instance routes by a host name that does not resolve from your machine, add `--integration-host <host>`.

:::note A decoder you just published takes up to a minute to be picked up
`event-sources` remembers which device types have a decoder for about a minute. An uplink sent inside that window can still be stored as the network server decoded it, and with no decoded object that means stored as nothing. If you rushed through the steps above, wait a minute and send again.
:::

## 8. See the result {#8-see-the-result}

Give it a few seconds after the simulator exits. The device's latest readings, with the units the profile declares:

```bash
gql device-state "$DC_TOKEN" '{latestMeasurements(deviceToken:"room-4"){name value unit}}' \
  | jq -r '.data.latestMeasurements[] | "\(.name) = \(.value) \(.unit // "")"'
```

```text
battery.voltage = 3.1 V
door = 1 
humidity = 57 %
lorawan.gateways = 2 
lorawan.rssi = -80 
lorawan.snr = 8.5 
temperature = 22.9 Cel
```

The four readings are the decoder's. The three `lorawan.` readings are the link metrics the integration appends. The last uplink, number twelve, reads 22.9: the sensor's temperature climbed past 25 and fell back, and the alarm followed it:

```bash
gql device-management "$DC_TOKEN" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state raisedTime clearedTime lastValue}}}' \
  '{"c":{"pageNumber":1,"pageSize":5,"originatorType":"device","originatorToken":"room-4"}}' \
  | jq '.data.alarms.results'
```

```json
[
  {
    "alarmKey": "room-too-warm",
    "severity": "WARNING",
    "state": "CLEARED",
    "raisedTime": "2026-11-06T20:43:01.144383Z",
    "clearedTime": "2026-11-06T20:43:09.16384Z",
    "lastValue": 25.4
  }
]
```

Uplink seven read 25.4 degrees, the first above 25, and that raised the alarm. Uplink eleven read 22.4 and cleared it, four uplinks and eight seconds later. Nothing in the rule says how to clear. In the console, the alarm is under **Monitor → Alarms**, and the readings are on the device's page.

If you do not see an alarm, send the twelve uplinks again after a minute, and read [Why isn't my device's data showing up](./why-isnt-my-data-showing-up.md).

## 9. The same with The Things Stack {#9-the-things-stack}

The Things Stack posts the same information in a different shape: the frame is `frm_payload`, the port is `f_port`, and the message type is the last segment of the path. Add a second integration for it, scoped to the same device type, and run the simulator as a The Things Stack:

```bash
export TTS_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"tts",name:"The Things Stack",type:"TTS_V3",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)

DC_SIM_INTEGRATION_TOKEN="$TTS_TOKEN" dc-simulator \
  --lorawan-ns tts --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/tts" \
  --deveui 70B3D57ED0050004 --uplinks 3 --emit-interval 1s
```

```json
{"ns":"tts","sent":3,"resent":0,"statuses":{"202":3},"errors":0,"runId":"1793997852808847000"}
```

The device, the decoder and the rule are the ones from the first run. The decoder reads `frame` and `fPort`, which both senders supply, so it did not change. Only the integration did, and the integration is where the sender's shape is understood.

## 10. When an uplink does not decode {#10-when-an-uplink-does-not-decode}

A decode that fails stores none of the uplink's readings, and is recorded as a failed event with the reason `DECODE_FAILED`. Send one on purpose: the same six bytes, on port 9, which the decoder's section does not accept. The simulator always uses port 2, so this one is sent by hand, as ChirpStack would send it:

```bash
curl -sS -i -X POST "$DC_URL/api/integrations/sim-lora/chirpstack?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"deduplicationId":"3f0c2f1e-6b1d-4a54-9a43-6a1f0f6f5e01","time":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
       "deviceInfo":{"devEui":"70b3d57ed0050004"},"fCnt":99,"fPort":9,"data":"AQD6LR8B"}' | head -1
```

```text
HTTP/1.1 202 Accepted
```

The network server is told `202`, because the integration accepted the message. The refusal is a record on your side. Read it with `dcctl events failed`, with the tenant, the identity and its password. Add `--server` with your host and port (`localhost:8080`, say) if the instance is not on `localhost` port 80; this command has no `--instance`:

```bash
dcctl events failed -t sim-lora --email lora@sim.devicechain.local --password "$PW"
```

```text
WHEN                         REASON         DEVICE  SOURCE             FOLDED  PAYLOAD    ERROR
2026-11-06T20:43:50.708786Z  DECODE_FAILED  room-4  device-management  0       AVAILABLE  decode_failed: no_section: no section matched this uplink

1 of 1 record(s).
```

The start of the error text is the reason from [the table of refusals](./payload-decoders.md#refusal-reasons): `no_section` here, because port 9 matched no section. A frame that is too short (`eval_error`) and a value outside a metric's limits (`invalid_output`) each have their own. The PAYLOAD column says whether the platform still holds the original message. The same records are under **Monitor → Ingest failures** in the console. The `deduplicationId` is fixed in this command; to send another failure, change its last digits, because a repeat inside ten minutes is dropped before it is processed.

## 11. Get told about it (optional) {#11-get-told}

An alarm that nobody sees is not much use. A **notification channel** is a place to send alarms, and a **policy** says which alarms go to which channel. This step sends the room-too-warm alarm to a webhook, which is also how you reach Slack or a paging service. Email over SMTP works the same way; the [channels guide](./notification-channels.md) has it.

You need a URL that accepts a `POST` and that the instance can reach over the public internet: a Slack incoming webhook, or an endpoint of your own. A receiver on your laptop will not do, because outbound calls from the instance refuse private and loopback addresses; an operator can [allow a specific address](../concepts/outbound-connectors.md#destinations) when one is needed. Put the URL in a variable. A Slack incoming webhook carries its credential in the URL, so the channel needs no separate secret (`"auth":"none"`):

```bash
export WEBHOOK_URL='https://hooks.example.com/alarms'

gql notification-management "$DC_TOKEN" 'mutation($r:NotificationChannelCreateRequest!){createNotificationChannel(request:$r){token channelType enabled}}'   "$(jq -nc --arg u "$WEBHOOK_URL" '{r:{token:"ops-hook",name:"Ops webhook",channelType:"webhook",config:({url:$u,auth:"none"}|tojson),enabled:true}}')"

gql notification-management "$DC_TOKEN" 'mutation($r:NotificationPolicyCreateRequest!){createNotificationPolicy(request:$r){token enabled rules{severity channel{token}}}}'   '{"r":{"token":"warm-rooms","name":"Warm rooms","enabled":true,"rules":[{"severity":"WARNING","channelToken":"ops-hook","recipients":"[]"}]}}'
```

```text
{"data":{"createNotificationChannel":{"token":"ops-hook","channelType":"webhook","enabled":true}}}
{"data":{"createNotificationPolicy":{"token":"warm-rooms","enabled":true,"rules":[{"severity":"WARNING","channel":{"token":"ops-hook"}}]}}}
```

The rule's severity is `warning`, in lower case, because that is how a detection rule is authored. The alarm it raises is `WARNING`, in upper case, and a notification rule matches the alarm's form. Policies are tenant-wide, and `recipients` is empty because a webhook has no recipients to name.

Now repeat [step 7](#7-send-uplinks). The twelve uplinks climb past 25 degrees again, the rule raises the alarm again, and the policy sends it. The receiver gets one `POST` as the alarm is raised:

```json
{
  "acknowledged": false,
  "alarmKey": "room-too-warm",
  "alarmToken": "e0a22e6d-08d9-4963-8124-5e672ccebdf3",
  "eventType": "RAISED",
  "metricKey": "temperature",
  "occurredTime": "2026-11-07T06:38:37Z",
  "originatorId": 1,
  "originatorType": "device",
  "raisedTime": "2026-11-07T06:38:37Z",
  "severity": "WARNING",
  "state": "ACTIVE",
  "text": "[WARNING] Alarm raised: room-too-warm on device 1",
  "value": 25.4
}
```

The alarm clearing four uplinks later sends nothing here: this policy notifies when an alarm is raised, and again if it stays unacknowledged and the policy escalates. The service also keeps a record of what it did for each alarm. Ask for it by the alarm's token, which is `alarmToken` in the message and `token` on the alarm in [step 8](#8-see-the-result):

```bash
gql notification-management "$DC_TOKEN" 'query($t:[String!]!){notificationStatesByAlarmToken(alarmTokens:$t){alarmToken firstNotifiedAt notifyCount}}'   '{"t":["e0a22e6d-08d9-4963-8124-5e672ccebdf3"]}'
```

```text
{"data":{"notificationStatesByAlarmToken":[{"alarmToken":"e0a22e6d-08d9-4963-8124-5e672ccebdf3","firstNotifiedAt":"2026-11-07T06:38:37.847456Z","notifyCount":1}]}}
```

If nothing arrives, the record is the first place to look: no record means the alarm did not reach the policy (check the severity), and a record with the receiver silent means the delivery was refused (check the URL is public). Throttling, escalation of an alarm nobody acknowledges, and quiet hours are in [Configuring notification channels](./notification-channels.md#policies).

## 12. Send a command back down {#12-send-a-command-back-down}

So far the rule only raises an alarm. A rule can also send the device a **command**. For a LoRaWAN sensor, the command goes back the way the uplink came. DeviceChain puts a downlink on the network server's queue, and the network server sends it after the device's next uplink. Then it reports whether the device acknowledged it. Here the simulator plays that side of ChirpStack as well. It serves ChirpStack's queue endpoint, and after the device's next uplink it posts the acknowledgement to the integration.

```text
 dc-simulator                         DeviceChain
┌──────────────────────┐   uplink     ┌──────────────────────────────────────────────────────┐
│ plays ChirpStack     │─────────────▶│ integration ──▶ decoder ──▶ rule  temperature > 25   │
│                      │              │                                │ sendCommand         │
│ queue endpoint       │◀─────────────│ lorawan_downlink connector ◀───┘ set-setpoint        │
│                      │   downlink   │                                                      │
│ next uplink, then    │─────────────▶│ integration ──▶ the command is SUCCESSFUL            │
│ txack and ack        │              │                                                      │
└──────────────────────┘              └──────────────────────────────────────────────────────┘
```

:::note What this step needs
The instance must run v1.4 or later, which added confirmed downlinks and the command encoder. The simulator flags used here are newer than that. Check that `dc-simulator --help` lists `--lorawan-downlink-listen`.
:::

**Switch the downlink on.** Three things are off on the instance you installed under [What you need](#requirements), and all three are set when you bootstrap the instance:

- the **outbound connectors**, the service that calls the network server. The default profile does not run it, so add it with `--enable-area outbound-connectors`.
- the **LoRaWAN downlink** itself, a setting of that service that you give in a values file.
- a **development egress allowance** for your own machine. The platform does not call private addresses, and the simulator's queue endpoint is on your laptop. `--dev-allow-egress` allows the address you name, and only on a local cluster such as kind.

Save the setting as `lorawan-downlink.yaml`:

```yaml
functionalAreas:
  outbound-connectors:
    config:
      lpwanDownlink:
        enabled: true
```

A pod reaches your machine as `host.docker.internal`, a name that Docker Desktop provides. Ask the cluster's node which address that name stands for:

```bash
docker exec devicechain-control-plane getent hosts host.docker.internal
```

```text
192.168.65.254  host.docker.internal
```

Then bootstrap the instance with all three, in place of the bootstrap command under [What you need](#requirements). Run it with `--dry-run` first. That changes nothing, and its summary prints `Profile: default (+ outbound-connectors)`:

```bash
dcctl bootstrap local devicechain --host localhost --no-tls --no-escrow \
  --enable-area outbound-connectors --values lorawan-downlink.yaml \
  --dev-allow-egress 192.168.65.254
```

On a new instance, repeat steps 1 to 6 afterwards. An instance that is already running takes the area with `dcctl enable-area`, and takes the other two from `dcctl upgrade` with the same `--values` and `--dev-allow-egress`. See the [dcctl reference](../reference/dcctl.md#dcctl-enable-area).

**Declare the command.** A command definition on the profile names the command, lists its parameters, and says how they become bytes. This one is the thermostat from the [encoder example](./inbound-integrations.md#lorawan-command-encoder), with `"confirmed": true` added so that the network server asks the device to acknowledge the frame. Save it as `encoder.json`:

```json
{
  "fPort": 12,
  "confirmed": true,
  "fields": [
    {"param": "setpoint", "type": "i16be", "scale": 0.1},
    {"bits": {"type": "u8", "fields": [
      {"param": "mode", "shift": 0, "width": 3}
    ]}}
  ]
}
```

```bash
gql device-management "$DC_TOKEN" 'mutation($r:CommandDefinitionCreateRequest!){createCommandDefinition(request:$r){token commandKey}}' \
  "$(jq -nc --rawfile e encoder.json '{r:{token:"room-sensor-set-setpoint",deviceProfileToken:"room-sensor",commandKey:"set-setpoint",name:"Set setpoint",parameterSchema:([{name:"setpoint",dataType:"DOUBLE"},{name:"mode",dataType:"INT"}]|tojson),encoder:$e}}')"
```

```text
{"data":{"createCommandDefinition":{"token":"room-sensor-set-setpoint","commandKey":"set-setpoint"}}}
```

The encoder turns the payload `{"setpoint":21.5,"mode":3}` into the three bytes `00d703` on port 12.

**Send it from the rule.** Add a `sendCommand` action to the rule, after the alarm. Save the whole rule as `rule-with-command.json`:

```json
{
  "name": "Room too warm",
  "description": "Above 25 degrees: raise a warning alarm and set the room's thermostat to 21.5 degrees. The alarm clears by itself when the reading drops back to 25 or below.",
  "type": "threshold",
  "severity": "warning",
  "when": { "metric": "temperature", "op": "gt", "threshold": 25 },
  "actions": [
    { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "room-too-warm" } },
    {
      "type": "sendCommand",
      "sendCommand": { "command": "set-setpoint", "payload": "{\"setpoint\":21.5,\"mode\":3}", "expiresAfter": "2m" }
    }
  ]
}
```

`expiresAfter` is how long the command waits for the device to answer. Two minutes is short on purpose, so that you can watch the deadline pass further down. Replace the rule's definition, then publish the profile again. The command and the new rule reach live uplinks only after you publish:

```bash
gql device-management "$DC_TOKEN" 'mutation($t:String!,$r:DetectionRuleUpdateRequest!){updateDetectionRule(token:$t,request:$r){token}}' \
  "$(jq -nc --rawfile d rule-with-command.json '{t:"room-too-warm",r:{definition:$d}}')"

gql device-management "$DC_TOKEN" 'mutation{publishDeviceProfile(token:"room-sensor",label:"thermostat command"){version}}'
```

```text
{"data":{"updateDetectionRule":{"token":"room-too-warm"}}}
{"data":{"publishDeviceProfile":{"version":2}}}
```

**Link the integration to its network server.** A connector of type `lorawan_downlink` holds two things for the `chirpstack` integration: where its network server takes downlinks, and the API key that server accepts. For ChirpStack you create the key in ChirpStack. The simulator accepts any string you choose, so make one and keep it in a variable:

```bash
export DC_SIM_LORAWAN_API_KEY=$(openssl rand -hex 24)

gql outbound-connectors "$DC_TOKEN" 'mutation($r:ConnectorCreateRequest!){createConnector(request:$r){token type}}' \
  "$(jq -nc --arg k "$DC_SIM_LORAWAN_API_KEY" '{r:{token:"chirpstack-downlink",name:"ChirpStack downlink",type:"lorawan_downlink",config:({networkServer:"chirpstack_v4",baseUrl:"http://host.docker.internal:8085",integration:"chirpstack"}|tojson),secret:$k}}')"

gql outbound-connectors "$DC_TOKEN" 'mutation{publishConnector(token:"chirpstack-downlink"){version}}'
```

```text
{"data":{"createConnector":{"token":"chirpstack-downlink","type":"lorawan_downlink"}}}
{"data":{"publishConnector":{"version":1}}}
```

`integration` is the integration's token from [step 6](#6-the-integration). The key is the connector's secret. It is stored encrypted and never shown again. The platform uses a connector only after it is published.

**Run the simulator with its queue endpoint.** `--lorawan-downlink-listen` serves ChirpStack's `POST /api/devices/{devEui}/queue` on your machine. It accepts only the key in `DC_SIM_LORAWAN_API_KEY` and refuses any other with `401`. Send the twelve uplinks of [step 7](#7-send-uplinks) again:

```bash
DC_SIM_INTEGRATION_TOKEN="$INTEGRATION_TOKEN" dc-simulator \
  --lorawan-ns chirpstack --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/chirpstack" \
  --deveui 70B3D57ED0050004 --uplinks 12 --emit-interval 2s \
  --lorawan-downlink-listen 127.0.0.1:8085
```

```json
{"ack":"ack","addr":"127.0.0.1:8085","event":"downlink_listening"}
{"event":"downlink_queued","devEui":"70B3D57ED0050004","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","fPort":12,"hex":"00d703","confirmed":true,"expiresAt":"2026-11-11T17:30:19Z"}
{"devEui":"70B3D57ED0050004","event":"downlink_reported","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","report":"txack","status":202}
{"acknowledged":true,"devEui":"70B3D57ED0050004","event":"downlink_reported","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","report":"ack","status":202}
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"queued":1,"reported":{"ack":{"202":1},"txack":{"202":1}},"runId":"1794418086190782000"}
```

Read it from the top. Uplink seven crossed 25 degrees, so the rule raised the alarm and sent `set-setpoint`. The platform queued the downlink with port 12, the bytes `00d703`, the confirmed flag and the command's expiry, and the simulator answered with a queue item id, as ChirpStack does. A class A device hears a downlink only just after it transmits, so the simulator holds the item until the device's **next** uplink. Then it posts what ChirpStack posts: a `txack`, which says a gateway sent the frame, and an `ack`, which says the device acknowledged it. Both name the queue item id, and the platform answered both with `202`. If a command is queued after the last of the twelve uplinks, the simulator keeps sending uplinks until it has reported on every confirmed item.

Now look at the command from the platform's side:

```bash
gql command-delivery "$DC_TOKEN" 'query($c:CommandSearchCriteria!){commands(criteria:$c){results{name payload status outcome sentTime respondedTime responsePayload deliveryEvidence}}}' \
  '{"c":{"pageNumber":1,"pageSize":5,"deviceToken":"room-4"}}' | jq '.data.commands.results'
```

```json
[
  {
    "name": "set-setpoint",
    "payload": "{\"mode\": 3, \"setpoint\": 21.5}",
    "status": "SUCCESSFUL",
    "outcome": "SUCCEEDED",
    "sentTime": "2026-11-11T17:28:19.101967Z",
    "respondedTime": "2026-11-11T17:28:20.555029Z",
    "responsePayload": "{\"stage\": \"acknowledged\", \"fCntDown\": 0, \"queueItemId\": \"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31\"}",
    "deliveryEvidence": "DEVICE_RESPONSE"
  }
]
```

The command became **Sent** when ChirpStack queued it. It stayed Sent until the acknowledgement arrived a second and a half later, after the next uplink, and only then became **Successful**, with the queue item id in its result. For a confirmed downlink, success means the device acknowledged the frame, not that the network server queued it.

**When the device does not answer.** `--lorawan-ack` chooses what the simulator reports. `ack` is the default. `nack` plays a network server that gave up waiting for the acknowledgement, and `none` one that reports nothing at all. Run the same twelve uplinks with `--lorawan-ack none`, and the simulator reports nothing back:

```json
{"ack":"none","addr":"127.0.0.1:8085","event":"downlink_listening"}
{"event":"downlink_queued","devEui":"70B3D57ED0050004","id":"b4051ecf-8c68-4b56-ac55-28207f50ed6e","fPort":12,"hex":"00d703","confirmed":true,"expiresAt":"2026-11-11T17:30:53Z"}
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"queued":1,"reported":{"ack":{},"txack":{}},"runId":"1794418119846090500"}
```

Run the same query with `pageSize:1`, the fields `name status outcome sentTime expiresAt respondedTime`, and `jq -c '.data.commands.results[]'`: once right after the run, and once after the two minutes are up. The command stays **Sent** until its deadline. Then it settles as **Timed out**, with the outcome unknown, because nothing says whether the device heard it:

```text
{"name":"set-setpoint","status":"SENT","outcome":null,"sentTime":"2026-11-11T17:28:53.106961Z","expiresAt":"2026-11-11T17:30:53Z","respondedTime":null}
{"name":"set-setpoint","status":"TIMEOUT","outcome":"UNKNOWN","sentTime":"2026-11-11T17:28:53.106961Z","expiresAt":"2026-11-11T17:30:53Z","respondedTime":null}
```

With `--lorawan-ack nack`, the command settles as **Timed out** as soon as the report arrives, with `{"reason": "NOT_ACKNOWLEDGED"}` as its result. [Confirmed downlinks](./inbound-integrations.md#lorawan-confirmed) lists what each report means for a command.

## The real network server {#real-network-server}

Replace the simulator with ChirpStack or The Things Stack, and the DeviceChain side stays as you built it. What changes is on the network server:

- **ChirpStack v4.** On the application, add the **HTTP** integration with payload encoding **JSON**, the event endpoint `https://<host>/api/integrations/<tenant>/chirpstack`, and the header `Authorization: Bearer <token>`. The device profile needs no codec: with a decoder published, the uplink is decoded by it, and what the network server decoded is not stored.
- **The Things Stack v3.** On the application, add a **Custom webhook** with the format **JSON**, the base URL `https://<host>/api/integrations/<tenant>/tts`, the header `Authorization: Bearer <token>`, and the uplink message path `/uplink-message`. The application needs no payload formatter, for the same reason.
- **Downlinks to ChirpStack.** Point the `lorawan_downlink` connector's `baseUrl` at ChirpStack's REST API (its `chirpstack-rest-api` component), and give it a ChirpStack API key that may enqueue downlinks. On the HTTP integration, enable the `ack` event, and `txack` if you want the transmission recorded: the acknowledgement reaches DeviceChain only through the integration you registered. Drop `--dev-allow-egress` when the network server has a public address; an operator allows a private one, as in [Outbound connectors](../concepts/outbound-connectors.md#destinations).
- **Devices.** Give each device in DeviceChain its DevEUI as a `lorawan.deveui` alias, or its device id as a `tts.device-id` alias when the integration identifies devices by `DEVICE_ID`. A DevEUI that no device carries is listed under **Unregistered devices seen** on the integration, and you can create the device from that list.
- **A public certificate.** A hosted network server will not trust a self-signed certificate, so the instance needs a certificate from a public CA before one can post to it. A network server you run yourself can be given your CA.

The sensor's own frames are the other change. The decoder in this guide reads the layout of the simulated sensor. For a real sensor, write a decoder from its datasheet, test it with real frames in `testPayloadDecoder`, and keep one saved vector for every layout it sends, so publishing proves the decoder still reads them. The field-by-field settings of both senders are in [Inbound integrations](./inbound-integrations.md#chirpstack), and a decoder over the object a network server has already decoded is a worked example in [Payload decoders](./payload-decoders.md#example-tts).

## Clean up {#clean-up}

The rule, the command, the device, the integrations and the connector belong to the tenant `sim-lora`. To remove the tenant and its identity:

```bash
dcctl sim destroy lora
```

If you are done with the instance, `dcctl destroy local devicechain` removes it.

## Related {#related}

- [Payload decoders](./payload-decoders.md): every part of a decoder, the function library, the test panel and the refusals.
- [Inbound integrations](./inbound-integrations.md): the senders, aliases, what is stored, and the limits.
- [Configuring notification channels](./notification-channels.md): email and webhook channels, policies, escalation and quiet hours.
- [Detection rules](../user-guide/detection-rules.md) and [Alarms](../user-guide/alarms.md): authoring rules and working the alarms they raise.
- [Why isn't my device's data showing up](./why-isnt-my-data-showing-up.md): the platform's record of what it refused.
