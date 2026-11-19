// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import { describe, it, expect } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';
import { isSuppressionUnavailable, optionalSuppression } from './optional-suppression';
const failure = (message: string) => new GraphQLRequestError(message, 200, [{ message }]);
describe('optional suppression API', () => {
  it.each([
    'Cannot query field "alarmSuppressionStatus" on type "Query".',
    'Cannot query field "alarmSuppressionsForDevice" on type "Query".',
    'Unknown type "AlarmSuppressionWindowSearchCriteria".',
    'Field "raisedDuringSuppression" is not defined by type "AlarmSearchCriteria".',
    'Cannot query field "raisedDuringSuppression" on type "Alarm".',
  ])('recognizes a schema refusal: %s', async message => {
    expect(isSuppressionUnavailable(failure(message))).toBe(true);
    expect(await optionalSuppression(() => Promise.reject(failure(message)))).toBeNull();
  });
  it.each(['Forbidden', 'alarmSuppressionStatus timed out', 'Cannot query field "typo" on type "Query".'])('preserves real failures: %s', async message => {
    expect(isSuppressionUnavailable(failure(message))).toBe(false);
    await expect(optionalSuppression(() => Promise.reject(failure(message)))).rejects.toThrow(message);
  });
  it('rethrows mixed validation and authorization errors', async () => {
    const error = new GraphQLRequestError('Mixed failure', 200, [
      { message: 'Cannot query field "alarmSuppressionStatus" on type "Query".' },
      { message: 'Forbidden' },
    ]);
    expect(isSuppressionUnavailable(error)).toBe(false);
    await expect(optionalSuppression(() => Promise.reject(error))).rejects.toBe(error);
  });
  it('requires a non-empty error array and accepts multiple compatibility refusals', () => {
    expect(isSuppressionUnavailable(new GraphQLRequestError('Empty', 200, []))).toBe(false);
    expect(isSuppressionUnavailable(new GraphQLRequestError('Old pod', 200, [
      { message: 'Cannot query field "alarmSuppressionStatus" on type "Query".' },
      { message: 'Cannot query field "raisedDuringSuppression" on type "Alarm".' },
    ]))).toBe(true);
  });
});
