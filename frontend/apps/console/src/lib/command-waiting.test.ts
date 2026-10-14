// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { commandWait } from './command-waiting';

const NOW = new Date('2026-10-13T12:00:00Z');
const FUTURE = '2026-10-14T02:00:00Z';
const PAST = '2026-10-13T02:00:00Z';

describe('commandWait', () => {
  it.each(['QUEUED', 'HELD', 'PARKED'])('is waiting while %s with a future dispatchAfter', (status) => {
    const wait = commandWait({ status, dispatchAfter: FUTURE, maintenanceWindowToken: 'night' }, NOW);
    expect(wait?.until.toISOString()).toBe('2026-10-14T02:00:00.000Z');
    expect(wait?.windowToken).toBe('night');
  });

  it('shows nothing for a dispatchAfter already in the past', () => {
    expect(commandWait({ status: 'QUEUED', dispatchAfter: PAST, maintenanceWindowToken: 'night' }, NOW)).toBeNull();
  });

  it('shows nothing at the exact instant the window opens', () => {
    expect(commandWait({ status: 'QUEUED', dispatchAfter: NOW.toISOString() }, NOW)).toBeNull();
  });

  it('shows nothing for a command that was never held', () => {
    expect(commandWait({ status: 'QUEUED', dispatchAfter: null }, NOW)).toBeNull();
    expect(commandWait({ status: 'QUEUED' }, NOW)).toBeNull();
  });

  it.each(['SENT', 'RESPONDED', 'CANCELLED', 'FAILED', 'EXPIRED'])(
    'shows nothing once the status is %s, whatever the stamp says',
    (status) => {
      expect(commandWait({ status, dispatchAfter: FUTURE }, NOW)).toBeNull();
    },
  );

  it('shows nothing for a stamp it cannot read', () => {
    expect(commandWait({ status: 'QUEUED', dispatchAfter: 'soon' }, NOW)).toBeNull();
  });

  it('carries no window token when the service recorded none', () => {
    expect(commandWait({ status: 'HELD', dispatchAfter: FUTURE }, NOW)?.windowToken).toBeNull();
  });
});
