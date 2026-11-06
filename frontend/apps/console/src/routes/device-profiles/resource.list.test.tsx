// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device-profile list leads with the profile's name and keeps the token as the
// secondary line. It used to list the token alone, so a named profile was identified by
// its id rather than by what it is called.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { deviceProfileResource } from './resource';
import type { DeviceProfile } from '@/lib/api/device-management';

afterEach(cleanup);

const profile = (over: Partial<DeviceProfile>): DeviceProfile =>
  ({ id: '1', token: 'thermostat-v2', name: null, description: null, category: null, deviceTypeCount: 0, ...over }) as DeviceProfile;

const identityCell = (p: DeviceProfile) => {
  const col = deviceProfileResource.columns[0];
  return { header: col.header, node: col.cell(p) };
};

describe('device-profile list identity column', () => {
  it('shows the name as primary text with the token beneath it', () => {
    const { header, node } = identityCell(profile({ name: 'Smart Thermostat' }));
    render(<div>{node}</div>);
    expect(header).toBe('common:colName');
    const name = screen.getByText('Smart Thermostat');
    const token = screen.getByText('thermostat-v2');
    expect(name.className).toContain('text-foreground');
    expect(token.className).toContain('text-muted-foreground');
    // Name first, token second.
    expect(name.compareDocumentPosition(token) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('falls back to the token alone for an unnamed profile', () => {
    const { node } = identityCell(profile({ name: null }));
    render(<div>{node}</div>);
    expect(screen.getAllByText('thermostat-v2')).toHaveLength(1);
    expect(screen.getByText('thermostat-v2').className).toContain('text-foreground');
  });
});
