# Contributing to DeviceChain

Thanks for your interest in improving DeviceChain! This guide covers the legal
prerequisites for contributing and the local checks your change must pass.

## Getting help

You do not need to contribute code to be useful to this project — telling us what broke
is a contribution.

| What you have | Where it goes |
| --- | --- |
| A question, or something that confused you | [Discussions](https://github.com/devicechain-io/devicechain/discussions) |
| An idea or feature request | [Discussions → Ideas](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) |
| Something is broken | [Open an issue](https://github.com/devicechain-io/devicechain/issues/new/choose) |
| A security vulnerability | Email **admin@devicechain.io** — see [below](#reporting-security-issues) |

DeviceChain is pre-1.0 and we would rather hear a rough report now than a polished one
after the interfaces are frozen. "I got stuck at step three and gave up" is a perfectly
good issue.

## Contributor License Agreement (required)

DeviceChain is stewarded by **IoT Innovations, LLC**. Before we can merge your
contribution, you must agree to our Contributor License Agreement (CLA). The CLA
grants us a broad license to your contribution — including the right to sublicense —
while **you keep ownership of your copyright**. This lets the project relicense or
dual-license in the future if needed, without having to track down every
contributor.

- **Individuals:** [ICLA.md](ICLA.md)
- **Companies** (or anyone contributing as part of their employment): have an
  authorized representative complete [CCLA.md](CCLA.md) and email it to
  **admin@devicechain.io**.

**How to sign:** When you open your first pull request, the **CLA Assistant** bot
will comment with a one-click link to review and sign the ICLA electronically. The
bot blocks merge until every commit author on the PR is covered. If you contribute
on behalf of an employer, make sure a CCLA is on file and your account is listed in
its Schedule B.

> Why a CLA and not just a DCO sign-off? A [Developer Certificate of
> Origin](https://developercertificate.org/) only certifies provenance; it does not
> grant the license rights the project needs to keep relicensing options open. We
> therefore use a CLA.

## Ground rules

- Be respectful and constructive. Assume good intent.
- Discuss non-trivial changes in an issue before opening a large PR.
- One logical change per PR; keep diffs focused and reviewable.

## License headers

Every new source file **must** begin with the two-line SPDX header:

```go
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
```

Copyright is attributed to **"The DeviceChain Authors"** — do **not** add a year or
attribute to any individual or company. For files with `//go:build` constraints, the
header goes above the build tag, separated by a blank line. Add or verify headers in
bulk:

```bash
# add header to any missing files
go run github.com/google/addlicense@latest -f hack/license-header.txt backend
# verify (CI enforces this)
go run github.com/google/addlicense@latest -check -f hack/license-header.txt backend
```

"DeviceChain" is a trademark of IoT Innovations, LLC — see [TRADEMARK.md](TRADEMARK.md)
before using the name or logo beyond ordinary referential use.

## Local checks (these are the CI gates)

**Run the Go checks with a module as the working directory, not the repo root.** The
root is not a Go module, so `go build ./...` there fails outright with `directory prefix
. does not contain modules listed in go.work`, and `gofmt -l .` reports files under
`_legacy/` — an archived tree that is deliberately not maintained. CI runs each module
separately, and so should you:

```bash
cd backend/core     # ...or whichever module you touched
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
```

To sweep every module before pushing, **save this to a file and run it** — it ends in
`exit`, so pasting it into your shell will close it. The workspace enumerates its own
modules, so the script never falls out of step with `go.work`:

```bash
rc=0
for m in $(go list -m -f '{{.Dir}}'); do
  ( cd "$m" || exit 1
    fmt="$(gofmt -l .)"; [ -z "$fmt" ] || { echo "not gofmt-clean:"; echo "$fmt"; exit 1; }
    go build ./... && go vet ./... && go test ./...
  ) || { echo "FAILED: $m"; rc=1; }
done
exit "$rc"
```

Two details in that script are load-bearing: `gofmt -l` exits 0 even when it names
files, so its *output* is tested rather than its status; and the loop records `rc=1`
rather than just printing, because `... || echo "FAILED: $m"` would make the loop's exit
status that of the `echo` — always 0 — so every module could fail and the sweep would
still look green.

Area-specific checks when you touch them:

```bash
# dcctl
cd backend/cli && make build

# frontend
cd frontend && npm ci && npm run codegen && npm run typecheck && npm test && npm run build

# helm — a bare `helm template` fails: any profile carrying a secret-store area needs
# an instance root key, so pass a throwaway one. (dcctl bootstrap mints the real one.)
helm lint deploy/helm/devicechain
helm template deploy/helm/devicechain \
  --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" >/dev/null

# opentofu
cd deploy/opentofu && tofu fmt -check -recursive && tofu init -backend=false && tofu validate
```

See [CLAUDE.md](CLAUDE.md) for the fuller repository guide (repository layout and
conventions).

## Changing the database schema

Four rules, and CI enforces three of them. They exist because DeviceChain services roll
one pod at a time against a database that is not versioned with them.

**1. Append a migration; never edit one that exists.** That includes the frozen baseline
each area starts from. A migration is a snapshot of a moment, not a description of the
current schema. Editing one silently changes what a *fresh* install builds while every
existing database applies cleanly and looks healthy — so the failure appears on someone
else's machine, weeks later, as `column already exists`.

There is one maintainer exception, mentioned so this page and the maintainer guide do not
disagree rather than because you should reach for it: a change that alters a migration's
**re-runnability alone**, and can prove with the schema gates that what a fresh install
builds does not move, is not the thing this rule exists to prevent. It has been used once.
If you are contributing, append.

**2. A migration declares its own structs.** Never point a migration at a live model.
The model is the current incarnation of the type; the migration is what the schema looked
like the day it was written. Wire the two together and every future model change quietly
rewrites history. Seeds count: insert through the snapshot type, with literal values.

**3. A migration must be individually re-runnable.** Migrations run outside a transaction
— TimescaleDB forbids DDL inside one — so a migration that fails partway is *not* rolled
back, and it replays from the top on the next boot. If the second run fails, the service
crash-loops on a half-built schema with no way forward.

In practice: `IF NOT EXISTS` on every `CREATE` and `ADD COLUMN`, `ON CONFLICT` on every
seed, and **name your indexes** — an unnamed `CREATE INDEX` does not fail on a replay, it
quietly creates a second index under a derived name, written on every insert and read by
nothing. `hack/migration-diff.sh replay` checks all of this, in CI, on every PR.

**4. Old and new schema must be readable by both old and new pods.** During a rolling
update the two run side by side against one database, so a change that only the new code
can read takes the old pods down while they are still serving. Expand first, contract
later: add the new column, ship code that writes both and reads either, and only remove
the old one in a *later* release once nothing reads it. A rename is a drop plus an add,
so it needs the same two steps.

The schema gates need Docker and take a few minutes, so they are not in the per-module
loop above:

```bash
hack/migration-diff.sh verify     # schemas still match their goldens; also runs the
                                  # replay and tenant-coverage gates and the purge drill
hack/migration-diff.sh snapshot   # refresh the goldens after an intended schema change
```

## Commit & PR conventions

- Use clear, imperative commit subjects; conventional-commit prefixes
  (`feat:`, `fix:`, `refactor:`, `docs:`) are used throughout the history.
- Reference related issues in the PR description.
- Ensure CI is green and the CLA check passes before requesting review.

## Reporting security issues

Please do **not** file public issues for security vulnerabilities. Email
**admin@devicechain.io** instead.

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE) and covered by the CLA above.
