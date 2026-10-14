// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { maintenanceWindowArg } from './windowArg';

describe('maintenanceWindowArg', () => {
  it('omits an unchanged window on an edit (omitted keeps; null would detach)', () => {
    expect(maintenanceWindowArg('night', 'night', true)).toBeUndefined();
    expect(maintenanceWindowArg('', '', true)).toBeUndefined();
  });

  it('attaches or switches with the token', () => {
    expect(maintenanceWindowArg('', 'night', true)).toBe('night');
    expect(maintenanceWindowArg('night', 'weekend', true)).toBe('weekend');
  });

  it('detaches with an explicit null', () => {
    expect(maintenanceWindowArg('night', '', true)).toBeNull();
  });

  it('on a create, names the window only when one is chosen', () => {
    expect(maintenanceWindowArg('', '', false)).toBeUndefined();
    expect(maintenanceWindowArg('', 'night', false)).toBe('night');
  });
});
