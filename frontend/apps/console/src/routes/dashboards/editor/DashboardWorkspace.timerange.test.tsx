// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console viewer's time range: the control in the header, the span the renderer is handed,
// the pause, and the author's board default.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { parseDashboardDefinition, type DeviceResolver } from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  timeRange: undefined as undefined | { spanSeconds: number; paused: boolean; pausedAt?: number },
}));

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  DashboardRenderer: (props: { timeRange?: { spanSeconds: number; paused: boolean; pausedAt?: number } }) => {
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
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
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

const range = (id: string) => document.querySelector(`[data-range="${id}"]`) as HTMLButtonElement;
const pause = () => document.querySelector('[data-time-range-pause]') as HTMLButtonElement;

describe('the console viewer time range', () => {
  it('hands the renderer the board default span, live', () => {
    mount();
    expect(h.timeRange).toEqual({ spanSeconds: 3600, paused: false });
    expect(range('1h').getAttribute('aria-pressed')).toBe('true');
  });

  it('starts at the span the author set as the board default', () => {
    mount({ timeRange: { defaultSeconds: 86400 } });
    expect(h.timeRange?.spanSeconds).toBe(86400);
    expect(range('24h').getAttribute('aria-pressed')).toBe('true');
  });

  it('a viewer pick changes the span the renderer draws', () => {
    mount();
    fireEvent.click(range('7d'));
    expect(h.timeRange).toEqual({ spanSeconds: 604800, paused: false });
  });

  it('pause freezes at the clock and says so in the header; resume lets go', () => {
    mount();
    act(() => void vi.advanceTimersByTime(5000));
    fireEvent.click(pause());
    expect(h.timeRange).toEqual({ spanSeconds: 3600, paused: true, pausedAt: NOW + 5000 });
    expect(screen.getByText(i18n.t('dashboards:timeRangePaused'))).toBeTruthy();
    fireEvent.click(pause());
    expect(h.timeRange).toEqual({ spanSeconds: 3600, paused: false });
  });

  it('is not shown in the editor (the author sees the layout, not a live range)', () => {
    mount();
    fireEvent.click(screen.getByRole('button', { name: /edit/i }));
    expect(document.querySelector('[data-time-range-bar]')).toBeNull();
  });
});
