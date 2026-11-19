// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import { GraphQLRequestError } from '@devicechain/client';

/** Only validation refusals prove that the serving pod predates this optional API. */
export function isSuppressionUnavailable(error: unknown): boolean {
  return error instanceof GraphQLRequestError && !!error.errors?.length && error.errors.every(({ message }) =>
    /Cannot query field "alarmSuppression\w*" on type "Query"|Cannot query field "raisedDuringSuppression" on type "Alarm"|Unknown type "AlarmSuppression\w*"|Field "raisedDuringSuppression" is not defined by type "AlarmSearchCriteria"/.test(message),
  );
}

export async function optionalSuppression<T>(read: () => Promise<T>): Promise<T | null> {
  try { return await read(); }
  catch (error) {
    if (isSuppressionUnavailable(error)) return null;
    throw error;
  }
}
