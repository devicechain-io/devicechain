---
name: device-anomaly-investigation
description: Use when someone asks why one DeviceChain device is behaving oddly - a reading that jumped or drifted, a device that went quiet, repeated alarms, or a command that seemed to change something. Compares the device against its own history within fixed query budgets, correlates commands, alarms and maintenance windows, and returns ranked explanations tied to tool results, with the one check a person should make next. Reads only.
license: Apache-2.0
metadata:
  tools: get_device get_device_capabilities get_device_state get_latest_measurements aggregate_measurements query_measurements list_commands list_alarms list_suppression_windows query_locations
---

# Device anomaly investigation

Explain one device's unusual behaviour from what the platform recorded, and say how sure you are.

Before the first call, read `references/ground-rules.md` (over MCP: `skill://device-anomaly-investigation/references/ground-rules.md`). It applies to every step below.

## Budget

- At most 6 calls to `aggregate_measurements` and 3 pages of `query_measurements`.
- At most 2 pages each of commands and alarms.
- About 25 tool calls in all.

## 1. Identify the device

You need the device's token. If the person gave a name, ask for the token or find it in results they point you to. Never guess a token from a name.

1. `get_device(tokens: [...])` with the token. If the token is not in the result, the device does not exist for this person; stop and say so.
2. `get_device_capabilities(deviceToken: ...)` to learn what the device measures (`metrics`, with each one's `dataType` and its `metricKey` under `untrusted`) and what it can be told to do (`commands`). If `activeVersion` is null, the profile has never been published and the listed metrics may not be active.

Agree with the person which metric and which time window the question is about. Tools address a metric by its key: the `metricKey` from the capabilities, which is the same string a reading shows as its `name`. A metric's display name is not its key. If they did not say, take the metric that changed and the last 24 hours.

## 2. Current picture

1. `get_device_state(deviceTokens: [...])`. Read `presenceSource` before `active`, and note `lastActivityTime` and `inactivityTimeout`.
2. `get_latest_measurements(deviceToken: ...)` for the current value and time of each metric.

If the device is not reporting, the question may be about silence rather than values. Say which of the two it is.

## 3. Compare the device with itself

Read `references/aggregation-budget.md` (over MCP: `skill://device-anomaly-investigation/references/aggregation-budget.md`) before choosing a bucket width.

1. The anomaly window: `aggregate_measurements(deviceToken: ..., name: ..., startTime: ..., endTime: ..., intervalSeconds: ...)`, with `name` set to the metric's key, copied exactly. If the result has no buckets at all, first check the key against the names `get_latest_measurements` returned before concluding that the device sent nothing.
2. A baseline: the same call over a comparable earlier window, of the same length, at the same time of day or day of week when the metric has a daily or weekly rhythm.
3. Compare count, average, minimum and maximum per bucket. A drop in `count` means fewer readings, which is a reporting problem rather than a value problem.
4. Only once you have found the change point, read raw values around it: `query_measurements(deviceToken: ..., startTime: ..., endTime: ..., pageSize: 100)` over a narrow window.

A `STRING` metric cannot be aggregated. Read `references/string-metrics.md` (over MCP: `skill://device-anomaly-investigation/references/string-metrics.md`) before using one.

## 4. Correlate

1. `list_commands(deviceToken: ..., pageSize: 25)`. Compare command times with the change point. A command with a `cause` was sent by an automation rule; `cause.detectedAt` and `cause.value` show what fired it.
2. `list_alarms(originatorToken: ..., pageSize: 25)`, with and without `state: ACTIVE` as needed. Compare raise and clear times with the change point.
3. If an alarm carries a `suppression`, that alarm was under maintenance. If the change fits a planned outage, call `list_suppression_windows(status: ACTIVE)`, or another status, without a target filter: a `targetToken` filter matches only windows that name that token directly and misses windows on the device's type, group, area, asset, customer or tenant. An empty filtered result does not rule out maintenance.
4. Only when the question is about movement or place, call `query_locations(deviceToken: ..., pageSize: 25)`. It needs the separate `location` scope and permission; if it is refused, carry on without positions and say so.

## 5. Conclude

Give a ranked list of explanations, most likely first. For each one:

- the tool results that support it, with times and numbers;
- what would contradict it;
- a confidence of high, medium or low.

Typical explanations: a real change in what the device measures; a sensor fault or drift; a reporting problem (fewer readings, a silent device, a slow interval); a command or automation that changed the device; planned maintenance.

End with the one check a person should make next, and what the tools could not see: whether the device is disabled, command payloads, anything outside the windows you read, and any step that was refused.
