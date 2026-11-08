---
sidebar_position: 3.7
title: Testing Your Rules
---

# Testing Your Rules {#testing-your-rules}

A detection rule that nobody has tested is a guess about what your devices will do. This guide shows how to write rule tests: you describe the events a fleet would send, state what the rules should do about them, and `dcctl test` checks the answer. The rules are evaluated by the platform's own detection engine, in event time, so a rule that waits seven days for silence is tested in a fraction of a second, and nothing it does touches your live devices, alarms or traffic.

:::note This feature is in preview
The scenario and test file formats carry the version `devicechain.io/ruletest/v1alpha1` and may change incompatibly in a minor release, with the change in that release's upgrade notes, until they are promoted. A file with a version the installed `dcctl` does not read is refused, and so is a key it does not know: a misspelled expectation that was ignored would pass.
:::

## What a test checks {#what-a-test-checks}

Two files make a test.

- A **scenario** (`scenario.yaml`) says what happens: which devices exist, what each one reports and when, and what goes wrong between the device and the platform.
- A **test file** (`*.test.yaml`) says what should follow: which alarms are raised or cleared, and when, which are never raised, and which side effect a rule should request.

The platform evaluates the scenario's rules over the events it generates and returns two things. The **edges** are the transitions the rules derived, each raised or resolved. The **intents** are the side effects those edges would cause: the alarm raise or clear, the command with its key and arguments, the webhook or published message with its rendered body. An intent is what the rule asks for. Nothing is executed. No alarm is raised, no command is enqueued, and no request leaves the platform.

Because the platform runs the rules, a test cannot disagree with production about what a rule means. `dcctl` holds no copy of the detection logic. What it holds is the part that has no place on a server: generating events from a seed, and judging a result against what you expected.

## Run your first test {#first-test}

Make a directory with two files. The scenario describes one boiler that is fine for half an hour, overheats, and then cools down.

```yaml title="tests/scenario.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler            # the token of the device profile whose rules are tested
start: 2026-03-01T00:00:00Z
duration: 2h
seed: 1
devices:
  - token: boiler-1
    streams:
      - metric: temperature
        every: 10m
        steps:
          - { at: 0s, value: 20 }
          - { at: 30m, value: 95 }
          - { at: 1h30m, value: 20 }
```

The test file says the published rules should raise an alarm when the boiler overheats, clear it once the boiler cools, and ask for the fan to be started.

```yaml title="tests/boiler.test.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: raised when the boiler goes over 80
    raised: { device: boiler-1, after: 25m, within: 10m }
  - name: cleared once it cools
    cleared: { device: boiler-1, after: 85m, within: 10m }
  - name: the fan is started
    intent: { kind: sendCommand, command: set-fan, payload: { speed: 3 } }
```

Run it as a member of the tenant. The identity needs `device:read`.

```bash
dcctl test ./tests --tenant acme --email you@example.com --password '<password>' --server localhost
```

The report names each expectation, and for a failure says what was found instead:

```text
evaluated tests/scenario.yaml: 13 events, 2 edges, 2 intents in 41ms
PASS  tests/boiler.test.yaml: raised when the boiler goes over 80
PASS  tests/boiler.test.yaml: cleared once it cools
FAIL  tests/boiler.test.yaml: the fan is started
        expected sendCommand, command "set-fan", payload {"speed":3} from any rule on any device to be emitted at least once, anywhere in the scenario; 0 matched; the intents emitted were: raiseAlarm "overheat" (rule hot, device boiler-1); clearAlarm "overheat" (rule hot, device boiler-1)
2 passed, 1 failed, 0 errored
```

The exit status is `0` when every expectation held and non-zero when one failed, when a file could not be read, or when the platform rejected the scenario. A directory with no `*.test.yaml` file is an error, because a run that checks nothing must not pass. To keep the result for a CI system, add `--junit report.xml`.

## Which rules are tested {#which-rules}

By default a scenario evaluates the profile's **published** rules: the ones live now. To test a rule before you publish it, evaluate a **draft** instead. Name a rule definition (the JSON a rule is stored as) or a canvas graph, as a file next to the scenario:

```yaml title="tests/scenario.yaml"
profile: boiler
ruleSet: DRAFT
draft:
  ruleDefinition: hot.rule.json      # or: graph: hot.canvas.json
```

A draft is evaluated over the events the scenario generates, so the profile does not have to be published. A canvas graph may hold several detection rules; each is evaluated, and named in the result by its condition node's id. A `ruleDefinition` draft is named `draft`.

A test names a rule with `rule:` in an expectation. For a published rule that is the rule's token.

## Writing a scenario {#scenario}

| Field | Meaning |
| --- | --- |
| `apiVersion`, `kind` | `devicechain.io/ruletest/v1alpha1` and `Scenario`. Required. |
| `profile` | The device profile whose rules are evaluated. Required. |
| `ruleSet` | `PUBLISHED` (the default) or `DRAFT`. |
| `draft` | For `DRAFT`: exactly one of `ruleDefinition` and `graph`, a path relative to the scenario. |
| `start` | The instant the scenario begins, as an RFC 3339 time. Required: a default of "now" would make every run different. |
| `duration` | How long it runs, such as `90m` or `7d`. Required. After the last event the engine's clock is moved to `start + duration`, which is what decides a rule that waits for silence. |
| `seed` | Seeds every random choice. The same seed always generates the same events. |
| `devices` | The simulated devices: a `token` and its `streams`. |
| `faults` | What goes wrong on the way to the platform. |

Durations take Go's units plus `d` (24 hours) and `w` (seven days): `90m`, `36h`, `7d`, `1d12h`. A bare number is refused.

### Streams {#streams}

A stream is one metric's series on one device. It names the metric, an `every` interval, and exactly one behaviour. `from` and `until`, offsets from `start`, bound the stream: one that stops before the end of the scenario is how a device goes quiet.

| Behaviour | Example | Produces |
| --- | --- | --- |
| `constant` | `constant: 21.5` | The same value at every sample. |
| `randomWalk` | `randomWalk: { start: 20, step: 0.5, min: 15, max: 30 }` | A walk that moves by a uniform amount in `[-step, +step]` each sample and stays inside `[min, max]`. |
| `steps` | `steps: [{ at: 0s, value: 20 }, { at: 1h, value: 95 }]` | A value that holds from each step until the next. The first step must be at `0s`. |
| `playback` | `playback: { file: recorded.csv }` | Readings from a recorded file. |

A playback file is CSV with a header row, or NDJSON (`.ndjson` or `.jsonl`). Each row has an `at` (an RFC 3339 time, or an offset from `start` such as `90m`) and a `value`, and may name a `metric`; a row that names none takes the stream's. The file is named relative to the scenario and cannot leave its directory.

```csv title="tests/recorded.csv"
at,value
0m,21.4
10m,21.9
2026-03-01T00:20:00Z,34.0
```

Readings of one device at the same instant are delivered as one event, which is what a device that reports several metrics in a sample sends.

### Faults {#faults}

A fault changes how events reach the platform, not what the device measured. A fault that names no `device` applies to every device.

| Fault | Example | Effect |
| --- | --- | --- |
| `gap` | `gap: { device: boiler-1, from: 1h, for: 30m }` | The samples in the window are lost for good. `metric` narrows it to one metric. |
| `offline` | `offline: { device: boiler-1, from: 1h, for: 30m }` | The device keeps sampling while disconnected and uploads when it reconnects: the samples keep their own times and arrive together at the end of the window, behind readings that occurred after them. |
| `duplicate` | `duplicate: { probability: 0.1 }` | Each event is delivered a second time with that probability. |
| `reorder` | `reorder: { probability: 0.2, maxDelay: 10m }` | Each event is delayed by a random time up to `maxDelay` with that probability, so it arrives behind later events. |

The difference between `gap` and `offline` matters: a gap tests what a rule does when data is missing, and an offline window tests what it does when the data arrives late.

Events are handed to the engine in the order they are delivered. The engine treats a late event as it does in production, and when it has to decline a reading that arrived too far behind the others, the result says so, and a failing expectation repeats the note.

## Writing a test {#test-file}

A test file has an `apiVersion` and `kind: RuleTest`, an optional `name`, an optional `scenario` (a path relative to the file; the default is `scenario.yaml` in the same directory), and a list of `expect` blocks. Each block names exactly one of:

| Block | Holds when |
| --- | --- |
| `raised` | At least one matching raised edge falls in the window. |
| `cleared` | At least one matching resolved edge falls in the window. |
| `neverRaised` | No matching raised edge falls in the window. |
| `intent` | At least one matching side effect is requested in the window. |
| `neverIntent` | No matching side effect is requested in the window. |

Every block takes an optional `name`, which labels it in the report and in JUnit. All of these narrow what counts as a match, and an omitted field matches anything:

- `rule` and `device` select the rule and the device.
- `after` and `within` give the window as offsets from the scenario's `start`: from `after` to `after + within`, inclusive at both ends. Leave out `within` to run to the end of the scenario.
- `count` (on `raised`, `cleared`, `intent`) requires exactly that many matches instead of at least one.

An `intent` block can also match what the side effect says:

| Field | Matches |
| --- | --- |
| `kind` | `raiseAlarm`, `clearAlarm`, `sendCommand`, `httpCall` or `publish`. |
| `edge` | `raised` or `resolved`: the edge that caused it. |
| `command`, `payload` | A command's key, and its argument object. The payload is compared as JSON, so key order and spacing do not matter, and a command with no arguments equals `{}`. |
| `alarmKey` | The key an alarm is raised or cleared under. |
| `connector`, `url`, `body`, `bodyContains` | The connector a message is published through, the endpoint a webhook calls, and the body the rule's template rendered, exactly or by a fragment. |

A command, a webhook and a published message are requested on a raised edge only; an alarm is cleared on the resolved edge of a rule that raises it.

## Tests that can fail {#tests-that-can-fail}

A test is worth nothing until you have seen it fail. After a test passes, plant a defect in the rule on purpose and check that the expectation that names it goes red: change a threshold, misspell a command key, invert a comparison. If a defect changes the rule and no expectation notices, the test is not checking what you thought. The planted-defect pack the platform itself ships with uses exactly this method: one scenario, six named expectations, and five deliberate defects, each of which must fail the expectations that name it and no others.

Windows are where tests are most often too loose. `raised: { device: boiler-1 }` passes if the boiler raised at any time, including the wrong one. Give the window a start and an end whenever the time matters.

## Run it in CI {#ci}

`dcctl test` needs an identity and a running instance, and it writes JUnit XML, which every CI system reads.

```bash
dcctl test ./tests \
  --tenant acme --email ci@example.com --password "$DC_PASSWORD" \
  --server platform.example.com --tls \
  --junit report.xml
```

Give the identity the least it needs: `device:read` on the tenant. If the account uses two-factor authentication, see `--otp` in the [command reference](../reference/dcctl.md#dcctl-test).

## What it does not model {#limits}

An evaluation is exact about the rules and honest about what it leaves out.

- **Device attributes.** A threshold that reads a device attribute never matches, because no device has attributes here. This is the same approximation the replay preview makes.
- **Group-scoped rules.** A published rule scoped to an entity group is skipped, and named as a warning, because a generated event carries no group memberships. A draft cannot be group-scoped.
- **Geofence rules.** A rule that tests geofence containment is refused, because the events carry no position.
- **Commands and connectors.** An intent is what the rule asks for. Whether the command exists on the device, or the connector exists in the tenant, is checked where the request is sent, not here.
- **Limits.** One evaluation takes at most 250,000 events, and a request body of 4 MiB by default; a scenario over either is refused before anything is evaluated, with the settings that made it large. It shares the replay preview's per-tenant concurrency limit, so a tenant cannot run unlimited evaluations at once.

## Determinism {#determinism}

The same scenario files always produce the same events, and the same events always produce the same result. Every random choice comes from a generator seeded by the scenario's `seed` and the identity of the thing it generates, so adding a device to a scenario does not change what another device reports. The evaluation reads no clock: a command's expiry is measured from the instant of the edge that caused it, not from the time you ran the test.

That is the property that makes a failure worth acting on. A failing test fails again, in the same way, until the rule or the expectation is fixed, and a fix is shown by the same run passing.
