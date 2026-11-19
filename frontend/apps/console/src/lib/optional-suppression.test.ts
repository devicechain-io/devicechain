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
  ])('recognizes a schema refusal: %s', async message => {
    expect(isSuppressionUnavailable(failure(message))).toBe(true);
    expect(await optionalSuppression(() => Promise.reject(failure(message)))).toBeNull();
  });
  it.each(['Forbidden', 'alarmSuppressionStatus timed out', 'Cannot query field "typo" on type "Query".'])('preserves real failures: %s', async message => {
    expect(isSuppressionUnavailable(failure(message))).toBe(false);
    await expect(optionalSuppression(() => Promise.reject(failure(message)))).rejects.toThrow(message);
  });
});
