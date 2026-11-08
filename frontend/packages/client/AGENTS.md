<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# AGENTS.md: @devicechain/client

Notes for an AI coding agent working in, or building on, this package.

## What it is

A small, framework-agnostic browser client for a DeviceChain instance: two-tier auth
(login, then tenant selection, then refresh), typed GraphQL over `fetch`, and live
subscriptions over `graphql-ws`. It has no Apollo, no store and no React. Read `README.md`
for usage.

## Rules that save time

- **Introspection is disabled by default on a running instance.** Do not try to discover the API by
  querying one. Read the published schemas (`/schema/index.json` on the documentation
  site, or `docs/static/llms.txt`) and generate typed documents from them.
- **Token handling goes through the token seam.** Never read or write a token anywhere
  else, and never put one in a URL.
- **The public surface is the root export.** `src/index.ts` is the contract; the package is
  published to npm, so a removal or rename there breaks callers. Add, and deprecate; do not
  remove in a minor release.
- **Keep it dependency-light.** The only runtime dependencies are `graphql-ws` and
  `@graphql-typed-document-node/core`; `graphql` is a peer. Do not add a framework.
- Every source file begins with the two-line SPDX header used across the repository.

## Commands

Run from `frontend/packages/client`:

```bash
npm run typecheck
npm test
npm run build
```
