---
title: Device bulk changes
---

# Device bulk changes

**Preview.** Use `dcctl bulk-change` to plan, create, start, inspect, pause, resume, abort or reverse a `config.set` change across devices, and to resolve lines that ended ambiguous. It sets or removes one declared SHARED configuration key.

Bulk changes are served from v1.10, on the device-management GraphQL API (`createBulkChange`, `startBulkChange`, `bulkChange` and the rest are listed in the [GraphQL schema reference](/reference/graphql/)) and through these commands. Both are Preview: they may change in a minor release. A server from before v1.10 answers **this instance does not serve bulk changes**. During a rolling upgrade to v1.10, schema discovery and the answering pod can differ, and only upgraded pods apply lines; **still upgrading; retry** means to wait and retry. An unavailable API is never reported as a missing token.

A change runs only once the instance is activated for it: `dcctl bulk-change readiness` lists `config.set` among the executable kinds when the database enforces the device attribute writer protocol and your tenant has no orphaned attribute rows. Start and resume check the same two facts.

## Access {#access}

Sign in as a tenant member with `--tenant`, `--email` and `--password`; use `--otp` when sign-in requires it. Select the host with `--server` and `--tls`, or use `--instance` to read its declared host and TLS setting.

Planning, creating, starting, resuming, reversing and resolving require `device:write` and `fleet:operate`. Pause and abort require `device:write`; reads require `device:read`. Agent/service identities and scoped members cannot use these bulk-change operations. The deprecated `dcctl fleet-op` spelling keeps `readiness` alone.

## Plan and create {#plan-and-create}

Prepare a UTF-8 file with one device token per nonempty line, or repeat `--device TOKEN`. Use exactly one target source. At most 10,000 input tokens and 1 MiB of file data are accepted.

```bash
cat > devices.txt <<'EOF'
meter-001
meter-002
EOF

dcctl bulk-change plan --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password>
dcctl bulk-change create --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password> -o json
```

The plan writes nothing and reports admitted/refused counts, refusal details and server digests. Create freezes the targets in READY and returns the contract and its digest. Missing devices become refused lines.

Use `--value V --type T` with STRING, LONG, DOUBLE, BOOLEAN or JSON, or `--unset` without a value or type. An empty STRING value is valid. Optional limits are `--max-posts-per-second` (default 10, maximum 50), `--failure-threshold` and `--ambiguity-threshold` (positive counts; omitted means no limit), and `--settle-deadline` (default 24h, range 60s–168h; durations or integer seconds).

The default token is SHA-256 of `tenant || exact file bytes || kind || canonical intent`. Repeated `--device` values use their ordered JSON array in place of file bytes. Repeating the identical request replays the original change; changing the request under the same token is a conflict. Line endings affect a file-derived token. Set `--token` to choose one explicitly and `--name` for a display name; the name otherwise defaults to the token. Changing limits with a default token can therefore produce a conflict: choose a new token for a new request.

## Review and start {#review-and-start}

Review the contract returned by create, then copy its exact server `contractDigest` into start. The client never computes an approval digest.

```bash
dcctl bulk-change start <token> --contract-digest <reviewed-server-digest> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change status <token> --watch --tenant acme --email operator@example.com --password <password>
```

Status always displays dispatch and evidence separately. Exit codes:

| Code | Meaning |
| --- | --- |
| 0 | DISPATCHED and evidence CLOSED |
| 1 | Still in progress without watch, an unknown state, an interruption or an error |
| 3 | PAUSED |
| 4 | ABORTED, even if evidence remains OPEN |

DISPATCHED with OPEN evidence is still waiting for device verdicts. Watch keeps polling until evidence closes, the change pauses or aborts, or you interrupt it. With `-o json`, each watch sample is one JSON object on its own line.

## Inspect lines {#inspect-lines}

The client reads every page in input ordinal order. Filters are server-side. JSON includes posting, settlement, evidence basis, revisions, errors and redaction facts; CSV quotes commas, quotes and newlines.

```bash
dcctl bulk-change lines <token> --posting POSTED --settlement AMBIGUOUS -o csv --tenant acme --email operator@example.com --password <password>
dcctl bulk-change lines <token> --devices-file devices.txt -o json --tenant acme --email operator@example.com --password <password>
```

Optional `--devices-file` joins the original file to ordinals and adds `inputToken` as caller-supplied provenance, separately from the server's `targetToken`. Use the original file, including its order. Duplicate tokens are normalized the same way as at create. Erased identifiers remain erased. An empty result can mean that lines have expired while the header remains; a missing header is an error.

## Pause, resume and abort {#pause-resume-and-abort}

```bash
dcctl bulk-change pause <token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resume <token> --max-posts-per-second 2 --tenant acme --email operator@example.com --password <password>
dcctl bulk-change abort <token> --tenant acme --email operator@example.com --password <password>
```

Pause stops new posts until resume. Omitted resume limits remain unchanged. Resume accepts the rate and threshold flags, and optional `--contract-digest`; without it, the client requests a server resume preview and uses that digest. This release refuses a wider contract or thresholds that remain exceeded.

Abort permanently cancels unposted lines. Posted lines keep settling and evidence can remain OPEN. A control-fence timeout is an error; inspect status before deciding what to do next.

## Reverse a bulk change {#reverse}

A reversal is a new bulk change that puts back, line by line, the value each device held before the original. Only a DISPATCHED or ABORTED bulk change can be reversed, and a reversal cannot itself be reversed.

```bash
dcctl bulk-change reverse <token> --token <reversal-token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change reverse <token> --token <reversal-token> --create -o json --tenant acme --email operator@example.com --password <password>
```

Without `--create` nothing is written: the server reports how many lines would be reversed, how many changed after the original was applied, how many devices were erased, how many lines another reversal already restored, how many were never applied, and the digests. With `--create` the reversal is created in READY. Review its contract and start it with its `contractDigest`, like any bulk change. Repeating `--create` with the same `--token` and the same request returns the first reversal.

`--token` is required and names the reversal; `--name` defaults to it. `--ordinals 1,4,9` reverses only those lines of the original; omitted, every applied line is reversed. The rate, threshold and `--settle-deadline` flags work as they do at create.

A line whose value changed after the original was applied is refused with CHANGED_SINCE, so a later change is never overwritten by accident. To apply it anyway, add `--override-changed` (every selected line) or `--override-ordinals 4,9` (only those lines). Creating a reversal with an override needs a second factor verified within the last five minutes; see [second factor](#second-factor).

## Resolve ambiguous lines {#resolve}

A line becomes AMBIGUOUS when no device evidence decided it before its deadline. A person can record a verdict on it:

```bash
dcctl bulk-change resolve <token> --ordinals 3,7 --verdict confirmed --note "checked on site" --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resolve <token> --all-ambiguous --verdict failed --note "devices offline since the outage" --tenant acme --email operator@example.com --password <password>
```

Select lines with `--ordinals` or every ambiguous line with `--all-ambiguous`. `--verdict` is `confirmed` or `failed`, and `--note` (1 to 1000 characters) says why. Lines that are not ambiguous are left unchanged. A verdict is a person's judgement, not device evidence: if a later report contradicts it, the line keeps the verdict and its evidence flag is set, visible in `lines -o json`. Resolving always needs a recent second factor.

## Second factor {#second-factor}

Resolving, and creating a reversal with an override, need your own sign-in with a second factor verified within the last five minutes. An account without a second factor is refused; set up an authenticator app first. Agent and service identities are always refused.

dcctl signs in when the command starts, so a code given with `--otp` is normally fresh enough. When the server still asks for a fresher one (for example, the sign-in used a recovery code, which does not count here), the command signs in again once and asks for a new authenticator code at the terminal. Without a terminal it stops and names `--otp`: run the command again with a new code. The request is sent again only after that refusal, which writes nothing.

See the [command reference](../reference/dcctl.md#dcctl-bulk-change) for every flag.
