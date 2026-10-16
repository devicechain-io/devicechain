// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { groupAuthorities, mayGrant } from './AuthorityChecklist';

describe('groupAuthorities', () => {
  it('groups by the area before the colon, sorted, with * left out', () => {
    expect(groupAuthorities(['device:write', '*', 'alarm:read', 'device:read', 'command:write'])).toEqual([
      { area: 'alarm', authorities: ['alarm:read'] },
      { area: 'command', authorities: ['command:write'] },
      { area: 'device', authorities: ['device:read', 'device:write'] },
    ]);
  });
});

describe('mayGrant', () => {
  it('grants what the editor holds, and everything to a holder of *', () => {
    expect(mayGrant(['device:read'], 'device:read')).toBe(true);
    expect(mayGrant(['device:read'], 'device:write')).toBe(false);
    expect(mayGrant(['*'], 'device:write')).toBe(true);
  });

  it('grants * only to a holder of *', () => {
    expect(mayGrant(['device:read', 'device:write'], '*')).toBe(false);
    expect(mayGrant(['*'], '*')).toBe(true);
  });
});
