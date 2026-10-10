// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { providerParamsArg } from './AiProviderForm';

// The server refuses a request that sets provider params (nothing applies them yet), so
// the editor must name `params` only when the operator actually changed it.
describe('providerParamsArg', () => {
  it('omits params that still equal the saved value', () => {
    expect(providerParamsArg('{"a":1}', '{"a":1}')).toBeUndefined();
    expect(providerParamsArg(' {"a":1} ', '{"a":1}')).toBeUndefined();
    expect(providerParamsArg('', '')).toBeUndefined();
  });

  it('sends null when cleared and the value when changed', () => {
    expect(providerParamsArg('', '{"a":1}')).toBeNull();
    expect(providerParamsArg('{"a":2}', '{"a":1}')).toBe('{"a":2}');
  });
});
