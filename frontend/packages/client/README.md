<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/client

The DeviceChain client SDK: a small, framework-agnostic core for talking to a
DeviceChain instance from the browser. Typed GraphQL over `fetch`, live
subscriptions over `graphql-ws`, and a token seam your app fills in.

There is no Apollo, no store and no React in here. It owns the wire; your app
owns state and UI.

```bash
npm install @devicechain/client graphql
```

`graphql` is a peer dependency — you almost certainly have it already, since the
typed documents this SDK accepts come from GraphQL Code Generator.

## Where the requests go

Each DeviceChain functional area serves its own GraphQL endpoint, and the SDK
addresses them **relative to the page**, as `/api/<area>/graphql`. That is the
contract a DeviceChain ingress serves: the SPA at `/`, each area behind
`/api/<area>`.

So an app served from the same origin as the instance needs no configuration at
all. An app served from somewhere else needs a dev-server proxy or a reverse
proxy that forwards `/api/<area>/...` to the instance — the same shape the
DeviceChain console's own `vite.config.ts` uses.

## Queries and mutations

```ts
import { gql } from '@devicechain/client';
import { DeviceListDocument } from './gql/graphql'; // your generated documents

const { devices } = await gql('device-management', DeviceListDocument, {
  first: 25,
});
```

The area is the first argument, and it is a checked union — `AREAS` is exported
as a real runtime array, not just a type, so you can enumerate it.

The result and variable types are inferred from the document. If the operation
takes no variables, the argument is optional.

Failures arrive as a `GraphQLRequestError` carrying the HTTP status and the
GraphQL error list. `isForbiddenError(err)` is the one classification worth
special-casing, because "you may not do that" is a different thing to show a
user than "the request did not arrive".

## Authentication

DeviceChain uses two tokens: a **tenant access token** for ordinary work, and an
**identity token** for instance-scoped admin endpoints. You register a getter for
each, and the SDK attaches the right one per request — admin areas are detected
from the area name, so a new admin call cannot quietly ride the tenant token.

```ts
import { setAuthTokenGetter, setIdentityTokenGetter } from '@devicechain/client';

setAuthTokenGetter(async () => store.accessToken); // refresh inside if near expiry
setIdentityTokenGetter(async () => store.identityToken);
```

Login and refresh calls run without a token; pass `{ anonymous: true }` for those.

`decodeToken`, `isExpired` and `hasAuthority` are exported for reading claims
client-side. They decode, they do not verify — the server is the authority.

## Subscriptions

```ts
import { subscribe, disposeSubscriptions } from '@devicechain/client';

const unsubscribe = subscribe(
  'event-management',
  MeasurementStreamDocument,
  { deviceToken: 'sensor-01' },
  {
    next: (data) => render(data),
    error: (err) => report(err),
    connected: (wasRetry) => setLive(true, wasRetry),
    closed: () => setLive(false),
  },
);

// on sign-out, drop every area socket so a stale token's connection cannot linger
disposeSubscriptions();
```

One socket is shared per area and reused across subscriptions. `connected` and
`closed` are connection-level signals, which is what lets a UI tell "connected
but idle" apart from "offline" — a distinction a data-only sink cannot make.

## Also in here

- **Basemap resolution** (`resolveBasemap`, `renderableBasemap`) — the client half
  of the map-tile cascade, so every surface that draws a map resolves a
  per-surface override over the tenant default the same way.
- **Token masks** (`generateToken`, `conformsToMask`, `parseMask`, …) — the
  device-token vocabulary, validated client-side before a round trip.

## Compatibility

ESM only, built for bundler resolution (Vite, webpack, Rollup, esbuild). The
emitted declarations use extensionless specifiers, which resolve under
TypeScript's `bundler` module resolution and **not** under `node16`/`nodenext`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
