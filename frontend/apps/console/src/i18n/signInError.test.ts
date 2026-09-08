// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE CASES BELOW ARE THE THROW SITES IN packages/client/src/transport.ts, READ OUT OF
// IT RATHER THAN IMAGINED. That is the whole strength of this file: a predicate over "what
// the SDK can throw" is only as good as the list of things the SDK can throw, and a
// hand-imagined list is exactly how `instanceof GraphQLRequestError` came to look
// sufficient on this screen.
//
// gql() throws four shapes of its OWN:
//
//   fetch() rejected            -> GraphQLRequestError(<browser text>, 0)           no errors
//   !res.ok                     -> GraphQLRequestError('Request failed (503)', 503)  no errors
//   body.errors non-empty       -> GraphQLRequestError(<server text>, 200, errors)   HAS errors
//   body.data undefined         -> GraphQLRequestError('Empty GraphQL response', 200) no errors
//
// …and a FIFTH it does not construct and does not catch: `await res.json()` REJECTS on a
// 200 whose body is not JSON — an ingress misroute serving the SPA's index.html under the
// API path, a captive portal — and that SyntaxError propagates to the caller as itself. It
// is not a GraphQLRequestError at all, so the predicate answers false and the screen says
// "unreachable", which is the right answer; the "not a GraphQLRequestError" cases below are
// where it is pinned. Worth naming rather than leaving as an unstated fifth, because "the
// four shapes" is the kind of closed list a later predicate gets written against.
//
// Only the third shape is the server saying no. Every other one is "no answer", however
// plausible its HTTP status looks.

import { GraphQLRequestError } from '@devicechain/client';
import { describe, expect, it } from 'vitest';

import i18n, { SUPPORTED_LOCALES } from './config';
import { serverRejectedRequest, signInErrorKey } from './signInError';

const REJECTED = 'login:invalidCredentials';

describe('serverRejectedRequest', () => {
  it('is true only when the server returned GraphQL errors', () => {
    const err = new GraphQLRequestError('invalid credentials', 200, [
      { message: 'invalid credentials' },
    ]);
    expect(serverRejectedRequest(err)).toBe(true);
  });

  // 🔴 EVERY ONE OF THESE IS A CASE THE TWO OBVIOUS PREDICATES GET WRONG, and each one is
  // a user being told to re-type a password that was never the problem.
  it.each([
    // `instanceof` alone says true here — the live defect: fetch() failing throws the
    // same class, with status 0.
    ['a dead network (fetch rejected)', new GraphQLRequestError('Failed to fetch', 0)],
    // `status !== 0` says true here: a 503 has a real status and no errors array. This is
    // the narrower fix that leaves the bug one status code over.
    ['an ingress 503', new GraphQLRequestError('Request failed (503)', 503)],
    ['a service 502', new GraphQLRequestError('Request failed (502)', 502)],
    // 200 with no data: the server answered, but not with a decision about us.
    ['an empty GraphQL response', new GraphQLRequestError('Empty GraphQL response', 200)],
    // An errors array that exists but is empty is not a rejection either.
    ['an empty errors array', new GraphQLRequestError('odd', 200, [])],
  ])('is false for %s', (_label, err) => {
    expect(serverRejectedRequest(err)).toBe(false);
  });

  it('is false for anything that is not a GraphQLRequestError at all', () => {
    expect(serverRejectedRequest(new Error('boom'))).toBe(false);
    expect(serverRejectedRequest('a string')).toBe(false);
    expect(serverRejectedRequest(null)).toBe(false);
    expect(serverRejectedRequest(undefined)).toBe(false);
  });
});

describe('signInErrorKey', () => {
  it('returns the caller’s rejection key when the server said no', () => {
    const err = new GraphQLRequestError('nope', 200, [{ message: 'nope' }]);
    expect(signInErrorKey(err, REJECTED)).toBe(REJECTED);
    expect(signInErrorKey(err, 'login:enterTenantFailed')).toBe('login:enterTenantFailed');
  });

  it('returns the unreachable key for every no-answer shape', () => {
    for (const err of [
      new GraphQLRequestError('Failed to fetch', 0),
      new GraphQLRequestError('Request failed (503)', 503),
      new Error('boom'),
    ]) {
      expect(signInErrorKey(err, REJECTED)).toBe('login:serverUnreachable');
    }
  });
});

// 🔴 THE HALF THAT MAKES THE REST MEAN ANYTHING. The key tests above would pass just as
// well against keys no catalog defines — i18next answers a missing key with the key's own
// name, so the user would read `invalidCredentials` as literal text on the screen that
// decides whether they can get in.
describe('every sign-in failure has real text in every shipped locale', () => {
  const KEYS = ['login:invalidCredentials', 'login:enterTenantFailed', 'login:serverUnreachable'];

  for (const { code } of SUPPORTED_LOCALES) {
    it.each(KEYS)(`${code}: %s renders as prose, not as its key`, async (key) => {
      await i18n.changeLanguage(code);
      const text = i18n.t(key);
      expect(text, `${code}/${key} is missing from the catalog`).not.toBe(key.split(':').pop());
      expect(text.length).toBeGreaterThan(0);
    });
  }

  // The control: the keys this file asserts must be the keys the code can actually
  // produce. Without it the list above could drift into testing three dead strings.
  it('covers exactly the keys signInErrorKey can return', () => {
    const rejected = ['login:invalidCredentials', 'login:enterTenantFailed'];
    const produced = new Set(
      rejected.flatMap((k) => [
        signInErrorKey(new GraphQLRequestError('x', 200, [{ message: 'x' }]), k),
        signInErrorKey(new Error('x'), k),
      ]),
    );
    expect([...produced].sort()).toEqual([...KEYS].sort());
  });
});
