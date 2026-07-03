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
  setAuthTokenGetter,
  setIdentityTokenGetter,
  GraphQLRequestError,
  type Area,
  type RequestOptions,
  type TypedDocument,
} from './transport';

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
  MAX_TOKEN_LEN,
  type GenerateOptions,
  type MaskSegment,
} from './tokens';
