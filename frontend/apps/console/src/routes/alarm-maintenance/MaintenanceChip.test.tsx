// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { MaintenanceChip } from './MaintenanceChip';
const until = '2099-01-01T16:00:00Z';
afterEach(cleanup);
describe('maintenance chip', () => {
  it('links only a visible window and uses its human name in the tooltip', () => {
    render(<MemoryRouter><MaintenanceChip suppression={{ until, windowToken: 'pump-work', window: { token: 'pump-work', name: 'Pump service', reason: 'Replace bearing' } }} /></MemoryRouter>);
    expect(screen.getByTitle('Pump service')).toBeTruthy();
    expect(screen.getByRole('link').getAttribute('href')).toBe('/alarm-maintenance/pump-work');
    expect(screen.getByRole('link').textContent).toMatch(/Maintenance.*until/);
  });
  it.each([
    { windowToken: 'restricted', window: null },
    { windowToken: 'restricted', window: { token: 'restricted', name: 'restricted', reason: 'restricted' } },
    { windowToken: 'deleted-window', window: null },
  ])('never exposes a hidden token or link: %s', hidden => {
    render(<MemoryRouter><MaintenanceChip suppression={{ until, ...hidden }} /></MemoryRouter>);
    expect(screen.getByTitle('Window not visible to you')).toBeTruthy();
    expect(screen.queryByRole('link')).toBeNull();
    expect(document.body.textContent).not.toMatch(/restricted|deleted-window/);
    expect(document.body.textContent).toMatch(/Maintenance.*until/);
  });
  it('omits an expired status', () => {
    render(<MaintenanceChip suppression={{ until: '2000-01-01T00:00:00Z', windowToken: 'restricted', window: null }} />);
    expect(document.body.textContent).toBe('');
  });
});
