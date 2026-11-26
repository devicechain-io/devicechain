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
- **The exit status is the summary's verdict:** `0` when the run succeeded or was planned (`--dry-run`), `1` otherwise, whatever a tool the install ran exited with. A run that fails unexpectedly inside dcctl still ends its stream with a failed summary and exits `1`.
- An `--output` value other than `text` or `json` is refused before anything runs, and so is a command line that does not parse (an unknown flag, for example). Neither writes a stream.

`--output text`, the default, is the output `dcctl install` has always printed, and it exits the same way: `0` or `1`.

`--help` is help, not a run: with `--output json` it is printed on stderr, and stdout stays empty.

## Prompts {#prompts}

The stream is non-interactive: **it never reads stdin**.

- `--yes` answers the one confirmation an install asks, which is whether to create the local kind cluster when it is missing. It settles it without asking, so a run with `--yes` has no `prompt.requested` or `prompt.resolved` events for it.
- Without `--yes`, that question cannot be answered. The run is refused at that point, before the cluster is created, with a `step.failed` event whose `code` is `run.prompt-unavailable`, whose `remedy` is `install.rerun-with-yes` and whose `effect` is `none_observed`. Re-run with `--yes`, or install into an existing cluster with `--kube-context`, which asks nothing.
- An install never asks for a secret, and `--yes` never supplies one.
- Nothing the install runs gets stdin either. A kubeconfig exec credential plugin runs non-interactively, and one that requires a terminal (`interactiveMode: Always`) fails instead of prompting.

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

The second column lists every field a payload of that kind can carry. A field that does not apply to an event is left out of it.

| `kind` | Fields | Meaning |
| --- | --- | --- |
| `run.started` | `target`, `dry_run`, `manifest` | `target` says where the run is going: its `mode` is `managed-kind` or `existing-context`, and its `context` names it (a context name is a label, not an identity). `manifest` lists the stages the run may execute |
| `step.started` | `message`, `params`, `budget_ms` | `message` is a code, with optional `params`. `budget_ms` is present when the stage has a wait bounded by a known budget |
| `step.progress` | `metric_id`, `mode`, `current`, `total`, `unit`, `source` | `mode` is `indeterminate` or `measured`, `total` is optional, and `unit` is `checks`, `resources`, `actions`, `deployments` or `replicas`. A value is a snapshot and may go down. Values are coalesced: see [Reading the stream](#reading-the-stream) |
| `step.finished` | `outcome`, `duration_ms` | `outcome` is `succeeded`, `unchanged`, `planned` (a dry run) or `not_applicable` |
| `step.failed` | `code`, `retry`, `remedy`, `remedy_params`, `cancelled`, `effect`, `duration_ms` | `retry` is `transient`, `permanent` or `unknown`. `remedy` is an optional code, with optional `remedy_params`. `effect` is `none_observed`, `may_have_changed` or `applied`. It never carries an error's text |
| `preflight.observation` | `observation`, `section`, `verdict`, `text`, `highlight`, `fix`, `fails`, `warns` | One row of the preflight report, and `observation` says which kind of row. `section`: a group of checks starts, named by the `section` code. `check`: one check, with its `verdict` (`pass`, `info`, `warn`, `fail`, `unknown`, `not_needed` or `not_checked`), its `text`, an optional `highlight` (a value the check calls out, such as the current kube context, shown after `text`) and, for a warning or a failure, a `fix`. `summary`: the report's closing `fails` and `warns` counts |
| `log.line` | `level`, `source`, `code`, `text` | One line of the install's own output, with terminal escapes removed. `code` is present when the line has one |
| `prompt.requested` | `id`, `prompt`, `question`, `params` | `prompt` is `confirm`, `choice` or `secret`. `question` is a code, with optional `params` |
| `prompt.resolved` | `id`, `disposition` | How the prompt ended, never the answer. An install resolves its one prompt as `unavailable` when no one could answer, or `cancelled` when the run was interrupted while asking |
| `run.cancelling` | `reason`, `active_step`, `waiting_for_tool`, `stop_budget_ms` | The run was interrupted: why, the stage it found, and whether it is waiting for a tool to stop and how long that tool was given |
| `run.summary` | `outcome`, `install_record`, `readiness`, `claim`, `failed_step`, `partial_state`, `next`, `credentials`, `delivery_incomplete` | The verdict. `outcome` is `succeeded`, `planned`, `failed` or `cancelled`. `partial_state` is an effect, as above. `next` lists commands to run next. `credentials` names the Kubernetes secrets that hold the credentials the install created (`namespace`, `name` and `key`), never their values. `delivery_incomplete` is `true` when an event could not be written, and such a run never reports success |

## Reading the stream {#reading-the-stream}

- **The last line is the verdict.** Every stream ends with exactly one `run.summary`. A stream that ends without one, because the process was killed for example, means the outcome is **unknown**, never success.
- **Only `run.summary` says whether the install succeeded.** A full progress count, a stage reached or a quiet log is not success.
- **Every started stage ends exactly once**, with `step.finished` or `step.failed`. A retry is a new attempt.
- **An interrupt is reported, then waited out, within a budget.** `run.cancelling` comes as soon as the interrupt arrives. The interrupted stage's `step.failed` (with `cancelled: true`) and the summary come once the work in flight has stopped, with one exception: an interrupted OpenTofu command that is not seen to finish within its graceful-stop budget and a margin after it. dcctl then stops waiting for it, the stage fails with `code` `run.tool-abandoned` and `effect` `may_have_changed`, and the summary follows while that command, or a provider plugin it started, may still be changing the cluster, even after dcctl has exited. dcctl does not start another run in the same process until such a command has exited, but a new `dcctl install` cannot see it: before re-running, make sure no OpenTofu process from the interrupted run is still running.
- **`effect` says what a failed stage may have left behind.** Only `none_observed` means nothing was changed. Re-running the same command converges whatever an interrupted or failed install left half done.
- **Log lines inform; they never decide.** They are bounded, and under heavy output the oldest may be dropped before a slow reader takes them.
- **Progress is coalesced.** When a stage reports progress faster than the stream is written or read, a newer value of the same metric replaces one not yet written, so intermediate values can be skipped. The last value a stage reported before it ended always arrives, ahead of its `step.finished` or `step.failed`.
- **Events of every other kind are never dropped.**
- **A command refused before its run could start** (conflicting flags, for example) writes one line: a `run.summary` with `outcome` `failed`, `failed_step` `install.inputs` and `partial_state` `none_observed`.
- **Read it leniently.** Later releases may add event kinds and fields within `dcctl.install.events/v1`; skip what you do not recognise. A change that would break a reader gets a new schema name.

## Secrets {#secrets}

The stream never carries the install's options, credentials or file contents. A prompt's resolution carries how it ended, never the answer.

As a second line of defence, in case a tool echoes one, these values are removed from any text dcctl prints and replaced by `[redacted]`:

- the secret values the install holds, such as an alert webhook URL, the SMTP password and backup credentials;
- the credentials in your kubeconfig: tokens, passwords, client keys, the password in a server or proxy URL (as written and URL-escaped), the values in exec plugin environments and auth provider settings, and the exec plugin arguments that hold a credential (the value of a flag such as `--client-secret` or `--token`, and an argument that looks like a key).

With `--output json`, that covers every event, the error printed on stderr (including a tool's diagnostics appended to it), and everything else that reaches dcctl's stdout or stderr while the install runs, including the stderr of a kubeconfig exec credential plugin. That stderr is redacted a line at a time, so it can arrive slightly later than the plugin wrote it.

There are limits:

- A value shorter than 4 bytes cannot be removed without removing ordinary text with it. It is not redacted, and the run says how many such values it found in a `warning:` log line.
- A credential from your kubeconfig is replaced only where it stands as a whole word, never inside a longer one, and it never changes what the run does. With `AWS_PROFILE=prod`, `--profile prod` shows `--profile [redacted]` while `production` is left alone, and a user the install never uses, with the password `install`, does not stop it. The secrets dcctl itself holds are replaced wherever they appear.
- With `--output text`, only the error dcctl prints is redacted. The rest is the output `dcctl install` has always printed, which can show you a credential on purpose (a bootstrap's access summary), and the tools it runs, kind and a kubeconfig exec credential plugin among them, write to the terminal as they always have.

A kubeconfig dcctl cannot read, or cannot turn into a client for a context, is reported by the kind of problem, the file and the context's name, never with the Kubernetes client's reason, which can quote the credentials and URLs in the file. This holds for `--output text` too.

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
