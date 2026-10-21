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
    follower: { type: 'device', scope: { parent: 'site', strategy: 'first' } },
    picked: { type: 'device', scope: { parent: 'site', strategy: 'manual' } },
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

  // 🔴 One parser reads the first of two values, another the last: a link that names a slot
  // twice means different devices to different readers, so it binds neither.
  it('refuses a slot named twice, whichever value is the visible one, and asks about neither', () => {
    for (const search of [
      'bind.machine=hauler-1&bind.machine=hauler-2',
      'bind.machine=hauler-1&bind.machine=hauler-1',
      'bind.machine=bad%20token&bind.machine=hauler-2',
      // The same key spelled two ways decodes to one key: still a duplicate.
      'bind.machine=hauler-1&bind.%6Dachine=hauler-2',
      'bind.machine=hauler-1&kiosk=1&bind.machine=hauler-2&bind.machine=hauler-3',
    ]) {
      const r = parseBindParams(search, definition);
      expect(r.binds, search).toEqual({});
      expect(r.issues, search).toEqual([{ kind: 'duplicate-slot', slot: 'machine', token: r.issues[0].token }]);
    }
  });

  it('a duplicate does not take the other slots of the link down with it', () => {
    const r = parseBindParams('bind.machine=a&bind.machine=b&bind.picked=hauler-3', definition);
    expect(r.binds).toEqual({ picked: 'hauler-3' });
    expect(r.issues.map((i) => i.kind)).toEqual(['duplicate-slot']);
  });

  // A slot that follows its parent ignores any pick, so a link to it would put one device in the
  // address and another on the screen.
  it('refuses a slot whose device is derived from its parent, and binds a manually scoped one', () => {
    const r = parseBindParams('bind.follower=hauler-1&bind.picked=hauler-2', definition);
    expect(r.binds).toEqual({ picked: 'hauler-2' });
    expect(r.issues).toEqual([{ kind: 'derived-slot', slot: 'follower', token: 'hauler-1' }]);
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
