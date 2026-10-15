// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The publish drawer's layout block: a placement outside what its widget can show, or off the
// canvas, is listed, disables Publish, and offers the "fit to limits" repair. The refusal itself is
// publishDashboard's (see lib/api/dashboards.test.ts); this is the explanation.

import '@/i18n/config';
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    gql: async (_area: string, doc: unknown) => {
      const op = /(?:query|mutation|subscription)\s+(\w+)/.exec(String(doc))?.[1] ?? '?';
      if (op === 'DashboardVersions') return { dashboardVersions: [] };
      if (op === 'DashboardPublishedVersion') return { dashboard: { publishedVersion: null } };
      return {};
    },
  };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { VersionHistorySheet } from './VersionHistorySheet';

afterEach(cleanup);

const gauge = (id: string, cols: number, rows: number, col = 0, row = 0) => ({
  id,
  type: 'gauge',
  layout: { base: { mode: 'grid' as const, col, row, cols, rows } },
  options: { title: id, min: 0, max: 10 },
});

function mount(widgets: unknown[], dirty = false) {
  const onFitToLimits = vi.fn();
  render(
    <ConfirmProvider>
      <VersionHistorySheet
        token="plant"
        open
        onOpenChange={vi.fn()}
        saved={parseDashboardDefinition({ title: 'Plant', widgets })}
        dirty={dirty}
        saving={false}
        canWrite
        expectedUpdatedAt="2026-10-11T09:00:00Z"
        onRolledBack={vi.fn()}
        onStripUnknownOptions={vi.fn()}
        onFitToLimits={onFitToLimits}
      />
    </ConfirmProvider>,
  );
  return onFitToLimits;
}

const publishButton = () => screen.getByRole('button', { name: /publish version/i }) as HTMLButtonElement;

describe('the publish drawer and layout', () => {
  it('lists a widget past its limits, blocks Publish and offers to fit', async () => {
    const onFit = mount([gauge('huge', 12, 12), gauge('fine', 3, 3, 0, 12)]);
    const block = await screen.findByTestId('publish-blocked-layout');
    expect(block.textContent).toContain('huge');
    expect(block.textContent).not.toContain('fine');
    expect(block.textContent).toContain('Size is outside what this widget supports');
    expect(publishButton().disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: /fit to limits/i }));
    expect(onFit).toHaveBeenCalledTimes(1);
  });

  it('names a footprint that runs off the grid', async () => {
    mount([gauge('edge', 4, 3, 10, 0)]);
    expect((await screen.findByTestId('publish-blocked-layout')).textContent).toContain('Runs past the right edge');
    expect(publishButton().disabled).toBe(true);
  });

  it('does not offer the repair against unsaved edits', async () => {
    mount([gauge('huge', 12, 12)], true);
    await screen.findByTestId('publish-blocked-layout');
    expect(screen.queryByRole('button', { name: /fit to limits/i })).toBeNull();
  });

  it('lets a board inside its limits publish', async () => {
    mount([gauge('fine', 3, 3)]);
    // Wait for the drawer body to settle on the publish control.
    await screen.findByRole('button', { name: /publish version/i });
    expect(screen.queryByTestId('publish-blocked-layout')).toBeNull();
    expect(publishButton().disabled).toBe(false);
  });
});
