---
sidebar_position: 4
title: Install event stream
---

# Install event stream {#install-event-stream}

`dcctl install --output json` (or `-o json`) runs the install exactly as it always runs, and reports it as a stream of events instead of text: one JSON object per line on stdout, in the order they happened. It is meant for automation, such as a CI job, a provisioning tool or a wrapper script, that needs to know which stage an install reached, what each stage did, and how the run ended, without parsing human output.

The stream's schema is `dcctl.install.events/v1`, and every line carries it.

## Running it {#running-it}

```bash
dcctl install local --kube-context prod --yes -o json > install.ndjson
echo "exit status: $?"
```

- **Stdout holds events and nothing else.** Every byte is part of a line, and every line is one complete JSON object. There is no banner, no colour and no terminal escape sequence. The install's own progress text arrives inside the stream as `log.line` events.
- **Stderr keeps the human-readable reason.** When a run fails, the same sentence the text output prints is printed on stderr, as it always was. Notices printed before the run starts go there too.
- **The exit status is unchanged:** `0` when the run succeeded or was planned (`--dry-run`), `1` otherwise.
- An `--output` value other than `text` or `json` is refused before anything runs, and so is a command line that does not parse (an unknown flag, for example). Neither writes a stream.

`--output text`, the default, is the output `dcctl install` has always printed.

## Prompts {#prompts}

The stream is non-interactive: **it never reads stdin**.

- `--yes` answers the one confirmation an install asks, which is whether to create the local kind cluster when it is missing.
- Without `--yes`, that question cannot be answered. The run is refused at that point, before the cluster is created, with a `step.failed` event whose `code` is `run.prompt-unavailable`, whose `remedy` is `install.rerun-with-yes` and whose `effect` is `none_observed`. Re-run with `--yes`, or install into an existing cluster with `--kube-context`, which asks nothing.
- An install never asks for a secret, and `--yes` never supplies one.

## The envelope {#envelope}

Every line is an envelope around one payload:

| Field | Meaning |
| --- | --- |
| `schema` | `dcctl.install.events/v1` |
| `run_id` | Random for each invocation; the same on every line of one run |
| `seq` | Starts at 1 and goes up by one per line, with no gaps |
| `time` | When the event happened, UTC, RFC 3339 |
| `elapsed_ms` | Milliseconds since the run started, from a monotonic clock |
| `operation` | `install` |
| `kind` | Which payload this is (see below) |
| `step_id` | The stage the event is about, when it is about one |
| `parent_id` | The stage's parent, for a stage nested in another |
| `attempt` | The stage's attempt, from 1, when the event is about one |
| `payload` | The event itself |

The stages of an install, in order, are `install.inputs`, `install.preflight`, `install.cluster`, `install.claim`, `install.inspect`, `install.credentials`, `install.prepare`, `install.write-intent`, `install.operator`, `install.prerequisites`, `install.database-login`, `install.record`, `install.rdb-ready` and `install.summary`. `run.started` lists them in its manifest. A stage that does not apply to a run still starts and finishes, as `not_applicable`.

## Event kinds {#event-kinds}

| `kind` | Payload |
| --- | --- |
| `run.started` | `target` (`mode` is `managed-kind` or `existing-context`, `context` names it; a context name is a label, not an identity), `dry_run`, and `manifest`, the stages the run may execute |
| `step.started` | `message` (a code), optional `params`, and `budget_ms` when the stage has a wait bounded by a known budget |
| `step.progress` | `metric_id`, `mode` (`indeterminate` or `measured`), `current`, optional `total`, `unit` (`checks`, `resources`, `actions`, `deployments` or `replicas`) and `source`. A value is a snapshot and may go down |
| `step.finished` | `outcome`: `succeeded`, `unchanged`, `planned` (a dry run) or `not_applicable`; and `duration_ms` |
| `step.failed` | `code`, `retry` (`transient`, `permanent` or `unknown`), optional `remedy`, `cancelled`, `effect` (`none_observed`, `may_have_changed` or `applied`) and `duration_ms`. It never carries an error's text |
| `preflight.observation` | One line of the preflight report: a `section`, a `check` with its `verdict` (`pass`, `info`, `warn`, `fail`, `unknown`, `not_needed` or `not_checked`), `text` and `fix`, or the closing `summary` with its `fails` and `warns` |
| `log.line` | `level`, `source` and `text`: one line of the install's own output, with terminal escapes removed |
| `prompt.requested` | `id`, `prompt` (`confirm`, `choice` or `secret`) and `question` (a code) |
| `prompt.resolved` | `id` and `disposition` only (`auto_confirmed` for `--yes`, `unavailable` when no one could answer). It never carries the answer |
| `run.cancelling` | The run was interrupted: `reason`, the `active_step`, and whether it is `waiting_for_tool` and for how long (`stop_budget_ms`) |
| `run.summary` | The verdict: `outcome` (`succeeded`, `planned`, `failed` or `cancelled`), `install_record`, `readiness`, `claim`, `failed_step`, `partial_state` (an effect, as above) and optional `next` commands |

## Reading the stream {#reading-the-stream}

- **The last line is the verdict.** Every stream ends with exactly one `run.summary`. A stream that ends without one, because the process was killed for example, means the outcome is **unknown**, never success.
- **Only `run.summary` says whether the install succeeded.** A full progress count, a stage reached or a quiet log is not success.
- **Every started stage ends exactly once**, with `step.finished` or `step.failed`. A retry is a new attempt.
- **An interrupt is reported, then waited out.** `run.cancelling` comes as soon as the interrupt arrives; the interrupted stage's `step.failed` (with `cancelled: true`) and the summary come only once the work in flight has stopped.
- **`effect` says what a failed stage may have left behind.** Only `none_observed` means nothing was changed. Re-running the same command converges whatever an interrupted or failed install left half done.
- **Log lines inform; they never decide.** They are bounded, and under heavy output the oldest may be dropped before a slow reader takes them. Events of every other kind are never dropped.
- **A command refused before its run could start** (conflicting flags, for example) writes one line: a `run.summary` with `outcome` `failed`, `failed_step` `install.inputs` and `partial_state` `none_observed`.
- **Read it leniently.** Later releases may add event kinds and fields within `dcctl.install.events/v1`; skip what you do not recognise. A change that would break a reader gets a new schema name.

## Secrets {#secrets}

The stream never carries the install's options, credentials or file contents. Secret values the install holds, such as an alert webhook URL, the SMTP password and backup credentials, are also removed from any text that reaches the stream, replaced by `[redacted]`, in case a tool echoes one. A prompt's resolution carries how it ended, never the answer.

## Example {#example}

An install with no kind cluster and no `--yes`, refused at the confirmation (manifest shortened):

```json
{"schema":"dcctl.install.events/v1","run_id":"…","seq":1,"time":"2026-11-26T12:00:00Z","elapsed_ms":0,"operation":"install","kind":"run.started","payload":{"target":{"mode":"managed-kind","context":"kind-devicechain"},"dry_run":false,"manifest":[{"id":"install.inputs"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":6,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.started","step_id":"install.cluster","attempt":1,"payload":{"message":"install.cluster"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":7,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.requested","step_id":"install.cluster","payload":{"id":"prompt-1","prompt":"confirm","question":"install.create-kind-cluster","params":[{"name":"cluster","value":"devicechain"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":8,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.resolved","step_id":"install.cluster","payload":{"id":"prompt-1","disposition":"unavailable"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":9,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.failed","step_id":"install.cluster","attempt":1,"payload":{"code":"run.prompt-unavailable","retry":"permanent","remedy":"install.rerun-with-yes","cancelled":false,"effect":"none_observed","duration_ms":0}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":10,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"run.summary","payload":{"outcome":"failed","install_record":"not_read","readiness":"not_checked","claim":"not_claimed","failed_step":"install.cluster","partial_state":"none_observed"}}
```

To follow a run as it happens and stop on its verdict:

```bash
dcctl install local --yes -o json | jq -c 'select(.kind == "step.started" or .kind == "run.summary")'
```

Every flag of `dcctl install` is in the [command reference](./dcctl.md#dcctl-install).
