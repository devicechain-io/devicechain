// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  bindParamsOf,
  bindsToSelection,
  parseBindParams,
  selectionToBinds,
  validateBinds,
  withBindParams,
} from './deep-link';
import type { DashboardDefinition } from './types';

const definition = {
  schemaVersion: 2,
  title: 't',
  slots: {
    machine: { type: 'device' },
    site: { type: 'anchor' },
  },
  widgets: [],
} as unknown as DashboardDefinition;

describe('parseBindParams', () => {
  it('reads a device slot bound to a well-formed token', () => {
    expect(parseBindParams('?bind.machine=hauler-1', definition)).toEqual({ binds: { machine: 'hauler-1' }, issues: [] });
  });

  it('sets aside, with a reason, what it cannot honour', () => {
    const r = parseBindParams('bind.nope=a&bind.site=b&bind.machine=bad%20token', definition);
    expect(r.binds).toEqual({});
    expect(r.issues.map((i) => i.kind)).toEqual(['unknown-slot', 'not-a-device-slot', 'invalid-token']);
  });

  it('ignores parameters that are not bind.*', () => {
    expect(parseBindParams('dashboard=x&kiosk=1', definition)).toEqual({ binds: {}, issues: [] });
  });

  it('does not treat a prototype name as a slot', () => {
    expect(parseBindParams('bind.__proto__=a&bind.constructor=b', definition).issues.map((i) => i.kind)).toEqual([
      'unknown-slot',
      'unknown-slot',
    ]);
  });
});

describe('validateBinds', () => {
  it('splits visible, absent and unchecked devices, and never throws', async () => {
    const resolver = {
      deviceExists: async (t: string) => {
        if (t === 'boom') throw new Error('network');
        return t === 'mine';
      },
    };
    const check = await validateBinds({ a: 'mine', b: 'theirs', c: 'boom' }, resolver);
    expect(check.ok).toEqual({ a: 'mine' });
    expect(check.missing).toEqual([{ slot: 'b', token: 'theirs' }]);
    expect(check.failed).toEqual([{ slot: 'c', token: 'boom' }]);
  });
});

describe('the selection round trip', () => {
  it('carries device picks on device slots only', () => {
    const sel = { machine: { kind: 'device' as const, deviceToken: 'h1' }, site: { kind: 'device' as const, deviceToken: 'x' }, ghost: { kind: 'device' as const, deviceToken: 'y' } };
    expect(selectionToBinds(sel, definition)).toEqual({ machine: 'h1' });
    expect(bindsToSelection({ machine: 'h1' })).toEqual({ machine: { kind: 'device', deviceToken: 'h1' } });
  });
});

describe('withBindParams', () => {
  it('replaces the bind.* parameters and leaves the rest in place', () => {
    expect(withBindParams('kiosk=1&bind.machine=old&theme=dark', { machine: 'new' })).toBe('kiosk=1&theme=dark&bind.machine=new');
  });
  it('is empty when nothing is left', () => {
    expect(withBindParams('bind.machine=old', {})).toBe('');
  });
  it('encodes a token rather than trusting it', () => {
    expect(withBindParams('', { machine: 'a&b=c' })).toBe('bind.machine=a%26b%3Dc');
  });
});

describe('bindParamsOf', () => {
  it('compares addresses regardless of parameter order', () => {
    expect(bindParamsOf('b=1&bind.y=2&bind.x=1')).toBe(bindParamsOf('bind.x=1&bind.y=2'));
  });
});
