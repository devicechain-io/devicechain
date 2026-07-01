# Contributing to DeviceChain

Thanks for your interest in improving DeviceChain! This guide covers the legal
prerequisites for contributing and the local checks your change must pass.

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

Run these from the repo root before pushing — a Go workspace resolves all modules, so
no vendor step is needed:

```bash
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
```

Area-specific checks when you touch them:

```bash
# dcctl
cd backend/cli && make build

# frontend
cd frontend && npm ci && npm run codegen && npm run typecheck && npm run build

# helm
helm lint deploy/helm/devicechain && helm template deploy/helm/devicechain >/dev/null

# opentofu
cd deploy/opentofu && tofu fmt -check -recursive && tofu init -backend=false && tofu validate
```

See [CLAUDE.md](CLAUDE.md) for the fuller repository guide (layout, conventions, and
the planning docs under `.agent-os/product/`).

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
