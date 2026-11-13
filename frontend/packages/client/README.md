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
import { DeviceListDocument } from './gql/graphql'; // generated query with a $criteria variable

const { devices } = await gql('device-management', DeviceListDocument, {
  criteria: { pageNumber: 1, pageSize: 25 },
});
```

The area is the first argument, and it is a checked union — `AREAS` is exported
as a real runtime array, not just a type, so you can enumerate it.

Use generated string documents (`TypedDocumentString`, with codegen's
`documentMode: 'string'`); the SDK calls `toString()` to obtain query text. An AST
`DocumentNode` is not printed into GraphQL by this transport. `TypedDocument<TResult,
TVariables>` is the exported contract for a hand-authored typed string.

The result and variable types are inferred from the document. If the operation
takes no variables, the argument is optional. To supply options for that case,
call `gql(area, document, undefined, options)`. The optional fourth argument is
`RequestOptions`: `anonymous` skips authentication; `identity` selects the
identity token explicitly. Both `/admin` and `/settings` areas default to the
identity token. `areaPath(area)` returns the HTTP path and `areaKey(area)` the
functional area before any slash.

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

`createSessionTokens` supplies the refresh logic for that getter. You tell it where
the token pair lives and how to call refresh. It refreshes shortly before the
access token expires, shares one in-flight refresh among concurrent callers
(refresh tokens rotate), and drops a result that arrives after the session was
replaced:

```ts
import { createSessionTokens, gql, setAuthTokenGetter } from '@devicechain/client';

const session = createSessionTokens({
  read: () => store.pair, // { accessToken, refreshToken } | null
  apply: (pair) => { store.pair = pair; },
  refresh: async (refreshToken) =>
    (await gql('user-management', REFRESH, { refreshToken }, { anonymous: true })).refresh,
  onEnd: (err) => {
    store.pair = null;
    showSignIn(err); // refused, lapsed, or offline past expiry
  },
});
setAuthTokenGetter(session.getAccessToken);
const stop = session.startAutoRefresh(); // optional: refresh on a timer, not only on demand
// On teardown: stop(). When signing out or replacing the pair: session.reset().
// onEnd must also clear store.pair; the SDK does not own that storage.
```

A refusal from the server, such as an invalid or revoked refresh token, ends the
session at once. A transient failure does not: no response, a 5xx, or a 429. While
the access token is still valid, callers keep getting it, and
`startAutoRefresh` retries with jittered backoff. The session ends only when the
access token actually expires.

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

The socket carries subscriptions only; send queries and mutations over HTTP. The
server closes it with code `4401` when the access token it opened with expires.
`subscribe` handles that close for you: it reconnects once with a freshly resolved
token, subscribes again, and reports the reconnect as `connected(true)`, so re-query
anything you must not miss when you see it.

For a subscription with no variables, the third argument is still required:
`subscribe(area, document, undefined, sink)`. Call the returned `unsubscribe()`
when its owner unmounts. The sink also accepts optional `complete()` for operation
completion. On sign-out, unregister the getters with `setAuthTokenGetter(null)`
and `setIdentityTokenGetter(null)` as well as disposing the sockets.

## Also in here

- **Basemap resolution** (`resolveBasemap`, `renderableBasemap`) — the client half
  of the map-tile cascade, so every surface that draws a map resolves a
  per-surface override over the tenant default the same way.
- **Token masks** (`generateToken`, `conformsToMask`, `parseMask`, …) — the
  device-token vocabulary, validated client-side before a round trip.

`errorCode(err)` reads the first GraphQL `extensions.code`, or returns null.
`isScopeUnsupportedError(err)` distinguishes an operation unavailable to scoped
members from an ordinary missing permission. `isScoped(claims)` and
`scopeAnchorRefs(claims)` read `DecodedClaims.scopeAnchors`; permissions are in
`authorities`, not a `scopes` field. `hasAuthority` takes decoded claims and a
required authority; `isExpired` takes a token and optional skew in seconds (30 by default).

`resolveBasemap(override, tenant)` returns the effective `Basemap`;
`renderableBasemap(basemap)` returns a renderable tile configuration or null, and
`fallbackView(basemap)` returns a center/zoom hint or null.

For token masks, `generateToken(mask, { seed?, random?, uuid? })` generates a token;
`conformsToMask(mask, token)` checks its shape. `normalizeToken(input)` normalizes
user text, `isValidToken(token)` checks the token grammar, and `validateMask(mask)`
returns a `MaskProblem` or null. `resolveMask(masks, entityType)` selects a configured
mask, falling back to `masks.default` and then `'{slug}'`. `parseMask`, `maskToRegExp`,
`sampleToken` and `MAX_TOKEN_LEN` support editors and previews.

`createSessionTokens` accepts optional `skewSeconds` (30 by default, capped at half
the token's lifetime). `read()` must return the same pair object until replacement,
so a late refresh can be discarded after a session switch.

## Compatibility

ESM only, built for bundler resolution (Vite, webpack, Rollup, esbuild). The
emitted declarations use extensionless specifiers, which resolve under
TypeScript's `bundler` module resolution and **not** under `node16`/`nodenext`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
