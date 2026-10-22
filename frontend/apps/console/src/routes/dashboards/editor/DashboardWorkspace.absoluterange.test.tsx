// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console viewer's absolute time range: a `?from=&to=` link opens the fixed window, an
// unusable one is ignored with a note, and a preset leaves the window for a live board.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { parseDashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  timeRange: undefined as undefined | { spanSeconds: number; paused: boolean; pausedAt?: number; absolute?: unknown },
}));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: (props: { timeRange?: { spanSeconds: number; paused: boolean; pausedAt?: number; absolute?: unknown } }) => {
    h.timeRange = props.timeRange;
    return null;
  },
  useDashboardHub: () => ({}),
  useSlotCandidates: () => undefined,
}));
vi.mock('./DashboardCanvas', () => ({ DashboardCanvas: () => null }));
vi.mock('./VersionHistorySheet', () => ({ VersionHistorySheet: () => null }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['dashboard:write'] } }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => false) }));
vi.mock('@/lib/api/dashboards', () => ({ updateDashboard: vi.fn(), deleteDashboard: vi.fn(), CONFLICT_MARKER: 'CONFLICT' }));

import { DashboardWorkspace } from './DashboardWorkspace';

const NOW = Date.parse('2026-10-14T12:00:00Z');

beforeEach(async () => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  h.timeRange = undefined;
  window.history.replaceState(null, '', '/');
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  window.history.replaceState(null, '', '/');
  vi.useRealTimers();
});

const board = (extra: Record<string, unknown> = {}) =>
  parseDashboardDefinition({
    schemaVersion: 3,
    title: 'Range',
    canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [],
    ...extra,
  });

function mount(extra: Record<string, unknown> = {}) {
  render(
    <MemoryRouter>
      <DashboardWorkspace token="dash-1" name="Range" updatedAt={null} loaded={board(extra)} resolver={{} as DeviceResolver} />
    </MemoryRouter>,
  );
}


describe('the console viewer absolute range', () => {
  it('opens a ?from=&to= link on a fixed window, live tail off, zone shown', () => {
    window.history.replaceState(null, '', '/?from=2026-10-14T02:00:00Z&to=2026-10-14T04:00:00Z');
    mount();
    expect(h.timeRange).toMatchObject({ spanSeconds: 7200, paused: true, pausedAt: Date.UTC(2026, 9, 14, 4) });
    expect(document.querySelector('[data-time-range-absolute]')?.textContent).toContain(i18n.t('dashboards:timeRangeAbsFixed'));
    fireEvent.click(document.querySelector('[data-range="6h"]') as HTMLButtonElement);
    expect(h.timeRange).toEqual({ spanSeconds: 21600, paused: false });
  });

  it('ignores an unusable link with a visible note and opens live', () => {
    window.history.replaceState(null, '', '/?from=2026-10-14T04:00:00Z&to=2026-10-14T02:00:00Z');
    mount();
    expect(h.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    expect(document.querySelector('[data-time-range-notice]')?.textContent).toContain(i18n.t('dashboards:timeRangeAbsProblemOrder'));
  });

  it('speaks Spanish', async () => {
    await i18n.changeLanguage('es');
    window.history.replaceState(null, '', '/?from=2026-10-14T02:00:00Z&to=2026-10-14T04:00:00Z');
    mount();
    expect(document.querySelector('[data-time-range-absolute]')?.textContent).toContain('Intervalo fijo');
    expect(screen.getByRole('button', { name: 'Volver a en vivo' })).toBeTruthy();
  });
});
