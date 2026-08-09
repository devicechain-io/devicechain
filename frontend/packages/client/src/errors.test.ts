// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { isForbiddenError } from './errors';
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
