// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { errorCode, isForbiddenError, isScopeUnsupportedError } from './errors';
import { GraphQLRequestError } from './transport';

describe('isForbiddenError', () => {
  it('recognizes the backend refusal message graphql-go surfaces over HTTP 200', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'forbidden: missing required authority' },
    ]);
    expect(isForbiddenError(err)).toBe(true);
  });

  it('recognizes a transport-level 403 with no GraphQL body to read', () => {
    expect(isForbiddenError(new GraphQLRequestError('Forbidden', 403))).toBe(true);
  });

  it('reads the top-level message when the response carries no errors array', () => {
    expect(isForbiddenError(new GraphQLRequestError('forbidden', 200))).toBe(true);
  });

  // The counterweight: a classifier that answered true often enough would turn every
  // outage into a permission boundary, which is the failure it exists to prevent.
  it('does NOT fire on an unrelated failure', () => {
    expect(isForbiddenError(new GraphQLRequestError('connection reset', 0))).toBe(false);
    expect(isForbiddenError(new GraphQLRequestError('boom', 500, [{ message: 'internal' }]))).toBe(false);
  });

  it('does NOT fire on an error that merely mentions the word', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'device "forbidden-zone-01" not found' },
    ]);
    expect(isForbiddenError(err)).toBe(false);
  });

  it('is false for anything that is not a GraphQLRequestError', () => {
    expect(isForbiddenError(new Error('forbidden'))).toBe(false);
    expect(isForbiddenError('forbidden')).toBe(false);
    expect(isForbiddenError(null)).toBe(false);
  });
});

describe('isForbiddenError by code', () => {
  it('recognizes the FORBIDDEN code whatever the message says', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'not available to a member limited to part of the tenant', extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } },
    ]);
    expect(isForbiddenError(err)).toBe(true);
  });

  it('does not treat another code as a refusal of authority', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'no such member', extensions: { code: 'NOT_FOUND' } },
    ]);
    expect(isForbiddenError(err)).toBe(false);
  });
});

describe('errorCode', () => {
  it('reads the first code the server set', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'plain' },
      { message: 'would leave nobody able to manage members', extensions: { code: 'LAST_ADMIN' } },
    ]);
    expect(errorCode(err)).toBe('LAST_ADMIN');
  });

  it('is null for an error with no code, and for a non-GraphQL throwable', () => {
    expect(errorCode(new GraphQLRequestError('boom', 500, [{ message: 'internal' }]))).toBeNull();
    expect(errorCode(new Error('LAST_ADMIN'))).toBeNull();
    expect(errorCode(null)).toBeNull();
  });
});

describe('isScopeUnsupportedError', () => {
  it('recognizes a refusal for a member limited to part of the tenant', () => {
    const err = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'not available to a member limited to part of the tenant', extensions: { code: 'FORBIDDEN', reason: 'SCOPE_UNSUPPORTED' } },
    ]);
    expect(isScopeUnsupportedError(err)).toBe(true);
  });

  // The counterweight: an ordinary refusal (a missing authority, a customer outside the
  // member's access) is not "not offered to customer users".
  it('does NOT fire on an ordinary refusal, another reason, or a non-GraphQL throwable', () => {
    expect(isScopeUnsupportedError(new GraphQLRequestError('GraphQL error', 200, [
      { message: 'forbidden: missing required authority', extensions: { code: 'FORBIDDEN' } },
    ]))).toBe(false);
    expect(isScopeUnsupportedError(new GraphQLRequestError('GraphQL error', 200, [
      { message: 'x', extensions: { code: 'NOT_FOUND', reason: 'SCOPE_UNSUPPORTED' } },
    ]))).toBe(false);
    expect(isScopeUnsupportedError(new GraphQLRequestError('Forbidden', 403))).toBe(false);
    expect(isScopeUnsupportedError(new Error('SCOPE_UNSUPPORTED'))).toBe(false);
  });
});
