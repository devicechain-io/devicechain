// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @devicechain/client — the DeviceChain client SDK (ADR-034/037).
//
// The framework-agnostic core of the client plane: the two-tier auth token seam,
// the typed GraphQL-over-fetch transport, JWT decode, and graphql-ws
// subscriptions. Hosts (the console, the dashboard app, sims) bring their own
// state/UI and register a token getter; the SDK owns the wire.

export {
  gql,
  areaPath,
  resolveAuthToken,
  setAuthTokenGetter,
  setIdentityTokenGetter,
  GraphQLRequestError,
  type Area,
  type RequestOptions,
  type TypedDocument,
} from './transport';

export { isForbiddenError } from './errors';

// The client half of the basemap cascade (ADR-079). Lives in the SDK because the
// console's fence editor, the map widget and the /dash viewer app must all resolve a
// per-surface override over the tenant value the SAME way — and /dash, with its own
// login, is the one most likely to quietly disagree.
export {
  resolveBasemap,
  renderableBasemap,
  fallbackView,
  type Basemap,
  type ResolvedBasemap,
} from './basemap';

export { subscribe, disposeSubscriptions, type SubscriptionSink } from './subscribe';

export {
  decodeToken,
  isExpired,
  hasAuthority,
  type DecodedClaims,
} from './jwt';

export {
  generateToken,
  normalizeToken,
  conformsToMask,
  maskToRegExp,
  parseMask,
  resolveMask,
  isValidToken,
  sampleToken,
  validateMask,
  MAX_TOKEN_LEN,
  type GenerateOptions,
  type MaskSegment,
  type MaskProblem,
} from './tokens';
