---
name: alarm-storm-triage
description: Use when a DeviceChain tenant has many open alarms at once and someone asks what is going on, which alarms belong together, or what can be acknowledged. Groups open alarms into incidents using platform fields, checks maintenance suppression, samples device evidence within a fixed budget, and reports each incident with its evidence and confidence. Reads only, unless the person asks for a proposal and the Preview proposal tool is listed.
license: Apache-2.0
metadata:
  tools: list_alarms get_alarm list_suppression_windows get_device get_device_state get_latest_measurements list_commands
  optional-tools: propose_alarm_action get_proposal
---

# Alarm storm triage

Turn a large set of open alarms into a short list of incidents. Each incident gets its evidence, its suppression status and a stated confidence.

Before the first call, read `references/ground-rules.md` (over MCP: `skill://alarm-storm-triage/references/ground-rules.md`). It applies to every step below.

## Budget

- At most 10 pages of alarms at 100 per page (1,000 alarms).
- At most 8 incidents examined in depth, and at most 5 sampled devices per incident.
- About 150 tool calls in all.

When a budget runs out, stop and report what is left as counts. Do not keep paging.

## 1. Scope the storm

Call `list_alarms(state: ACTIVE, acknowledged: false, pageSize: 100)`, then the following pages with `pageNumber`, until a page comes back short or the budget is reached. Record `totalRecords` from the first page so you can say how much of the storm you saw. The list is live: alarms are raised and cleared while you page, so an alarm can appear on two pages or be skipped. Keep each alarm once, by its `token`.

Note `suppressionUnavailable` on every page. If it is ever true, suppression is unknown for that page's alarms.

## 2. Group alarms into incidents

Group only on values the platform computes or on exact equality of identifiers:

- the alarm key: the same `alarmKey` value, compared as an exact string, never by what it means;
- `severity`;
- the device type of the originating device: look up the distinct `originatorToken` values with `get_device(tokens: [...])`, at most 50 tokens per call, and read `deviceType`;
- the raise time: alarms raised within the same 15-minute window belong together more often than not.

Do not group on what a device name, metric name or alarm key appears to say. Two alarms whose keys read alike but differ are different keys.

Rank the groups by size, then by the highest severity in the group (CRITICAL, MAJOR, MINOR, WARNING, INDETERMINATE). Take the top 8 as incidents to examine. Report the rest as counts per alarm key and severity.

## 3. Separate the suppressed

For each incident, count the alarms that carry a `suppression` (a maintenance window and its `until` time). Then call `list_suppression_windows(status: ACTIVE)` once, and page it if needed, to see which windows are in force and what they target.

- An alarm with a `suppression` is under maintenance. Report it as such, not as a fault to chase.
- If `suppressionUnavailable` was true, say that suppression is unknown for those alarms. Never report them as not suppressed.
- A `windowToken` of `restricted` is a window you may not see. Count it as suppressed and say the window is hidden from you.

## 4. Sample evidence for each incident

For each incident, pick up to 5 originating devices: the earliest raised, the latest raised, and up to 3 others spread across the group.

1. `get_device_state(deviceTokens: [...])` for the sample, in one call. Read `presenceSource` before `active`: an inferred inactive device is not reporting, which is not the same as down.
2. `get_latest_measurements(deviceToken: ...)` for each sampled device, to compare the current values with each alarm's `lastValue`.
3. `list_commands(deviceToken: ..., pageSize: 10)` for each sampled device. A command with a `cause` was sent by an automation rule. A recent command, or a rule that fired just before the alarms, is a likely trigger.

Use `get_alarm(tokens: [...])` only when an alarm in the list lacks a detail you need.

If the sampled devices disagree with each other, the incident probably mixes two causes. Say so rather than forcing one explanation.

## 5. Report

For each incident, give:

- what groups it: alarm key, severity, device type, time window, and how many alarms and devices;
- how many of its alarms are suppressed, not suppressed, or unknown;
- a hypothesis (for example, a site lost connectivity, a sensor model drifts, an automation fired in a loop, a planned maintenance), with the tool results that support it and a confidence of high, medium or low;
- the one check a person should make next.

Then state what you did not examine: alarms beyond the budget, incidents not sampled, and any step skipped because a tool was missing or refused.

Do not recommend clearing an alarm as a way to quiet a condition that is still true.

## 6. Propose an acknowledgement (only when asked)

Do this step only when the person asks for it, and only when `propose_alarm_action` is in your tool list. If it is not listed, say that agent actions are not available to you and stop.

Read `references/proposing.md` (over MCP: `skill://alarm-storm-triage/references/proposing.md`), then for each incident the person wants acknowledged:

1. `propose_alarm_action(action: ack, alarmTokens: [...], requestId: ..., dryRun: true)` with at most 100 alarm tokens.
2. Read the `verdict`, `requiredApprovals`, `reasons` and `preview`. A `REFUSE` verdict is the answer for that incident.
3. If the verdict is `APPROVE`, submit the same action and tokens with `dryRun: false` and a new `requestId` made for this submission. If that call fails or times out, retry it with that same `requestId`, unchanged.
4. Tell the person the proposal token, `digest` and `expiresAt`, and that a person approves it in the console Approvals inbox. Follow it with `get_proposal(token: ...)` when the person asks.
