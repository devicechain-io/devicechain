// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Rolling back to, or re-serving, an OLDER version that carries widgets this console cannot
// resolve is not blocked (only publishing a new version is), but the author is told which
// widgets before they confirm. Driven over the real drawer and confirm dialog; only the GraphQL
// transport is faked.

import '@/i18n/config';
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const net = vi.hoisted(() => ({ ops: [] as string[], definition: '', definitionFails: false }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    gql: async (_area: string, doc: unknown) => {
      const op = /(?:query|mutation|subscription)\s+(\w+)/.exec(String(doc))?.[1] ?? '?';
      net.ops.push(op);
      switch (op) {
        case 'DashboardVersions':
          return {
            dashboardVersions: [
              { version: 2, label: 'newer', description: null, publishedAt: '2026-10-11T10:00:00Z', publishedBy: 'a' },
              { version: 1, label: 'older', description: null, publishedAt: '2026-10-10T10:00:00Z', publishedBy: 'a' },
            ],
          };
        case 'DashboardPublishedVersion':
          return { dashboard: { publishedVersion: 2 } };
        case 'DashboardVersionDefinition':
          if (net.definitionFails) throw new Error('version read refused');
          return { dashboardVersion: { definition: net.definition } };
        case 'RollbackDashboard':
          return { rollbackDashboard: { definition: net.definition, updatedAt: '2026-10-12T10:00:00Z' } };
        case 'ActivateDashboardVersion':
          return { activateDashboardVersion: { publishedVersion: 1 } };
        default:
          return {};
      }
    },
  };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { VersionHistorySheet } from './VersionHistorySheet';

const GHOST = {
  title: 'Plant',
  widgets: [
    {
      id: 'far',
      type: 'acme/far-widget',
      widget: { version: '3.0.0', optionsVersion: 1 },
      layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0 } },
      options: { title: 'far' },
    },
  ],
};
const CLEAN = { title: 'Plant', widgets: [] };

function mount(onRolledBack = vi.fn()) {
  render(
    <ConfirmProvider>
      <VersionHistorySheet
        token="plant"
        open
        onOpenChange={vi.fn()}
        saved={parseDashboardDefinition(CLEAN)}
        dirty={false}
        saving={false}
        canWrite
        expectedUpdatedAt="2026-10-11T09:00:00Z"
        onRolledBack={onRolledBack}
        onStripUnknownOptions={vi.fn()}
      />
    </ConfirmProvider>,
  );
  return onRolledBack;
}

beforeEach(() => {
  net.ops.length = 0;
  net.definitionFails = false;
});

const confirmIn = (dialog: HTMLElement) => {
  const buttons = dialog.querySelectorAll('button');
  fireEvent.click(buttons[buttons.length - 1]);
};
afterEach(cleanup);

describe('rolling back to a version with unresolved widgets', () => {
  it('warns, naming the widgets, but does not block the rollback', async () => {
    net.definition = JSON.stringify(GHOST);
    const onRolledBack = mount();
    const rollbacks = await screen.findAllByRole('button', { name: /roll back/i });
    fireEvent.click(rollbacks[1]); // version 1

    const warning = await screen.findByTestId('version-unresolved-warning');
    expect(warning.textContent).toContain('far');
    expect(warning.textContent).toContain('acme/far-widget');

    // Confirming still performs the rollback.
    const dialog = screen.getByRole('dialog');
    const confirmButtons = dialog.querySelectorAll('button');
    fireEvent.click(confirmButtons[confirmButtons.length - 1]);
    await waitFor(() => expect(onRolledBack).toHaveBeenCalledTimes(1));
    expect(net.ops).toContain('RollbackDashboard');
  });

  it('shows no warning for a version that resolves fully', async () => {
    net.definition = JSON.stringify(CLEAN);
    mount();
    const rollbacks = await screen.findAllByRole('button', { name: /roll back/i });
    fireEvent.click(rollbacks[1]);
    await screen.findByRole('dialog');
    expect(screen.queryByTestId('version-unresolved-warning')).toBeNull();
  });

  it('asks before re-serving a version with unresolved widgets, and goes ahead on confirm', async () => {
    net.definition = JSON.stringify(GHOST);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /make live/i }));
    expect((await screen.findByTestId('version-unresolved-warning')).textContent).toContain('far');
    expect(net.ops).not.toContain('ActivateDashboardVersion');
    const dialog = screen.getByRole('dialog');
    const buttons = dialog.querySelectorAll('button');
    fireEvent.click(buttons[buttons.length - 1]);
    await waitFor(() => expect(net.ops).toContain('ActivateDashboardVersion'));
  });

  it('re-serves a version with nothing to report without asking, as before the check existed', async () => {
    net.definition = JSON.stringify(CLEAN);
    mount();
    fireEvent.click(await screen.findByRole('button', { name: /make live/i }));
    await waitFor(() => expect(net.ops).toContain('ActivateDashboardVersion'));
    expect(net.ops).toContain('DashboardVersionDefinition');
    // The drawer is itself a dialog; what must not appear is the confirmation.
    expect(screen.queryByText(/serve version 1/i)).toBeNull();
  });

  it('names a widget whose package is installed by the package title, with its type and reason', async () => {
    net.definition = JSON.stringify({
      title: 'Plant',
      widgets: [
        {
          id: 'g1',
          type: 'gauge',
          // Saved under a settings version the installed gauge does not use.
          widget: { version: '1.0.0', optionsVersion: 99 },
          layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 3, z: 0 } },
          options: {},
        },
      ],
    });
    mount();
    fireEvent.click((await screen.findAllByRole('button', { name: /roll back/i }))[1]);
    const warning = await screen.findByTestId('version-unresolved-warning');
    expect(warning.textContent).toContain('Gauge');
    expect(warning.textContent).toContain('(gauge)');
    expect(warning.textContent).toContain('settings version');
  });

  it('says the check could not run when the version cannot be read, and still rolls back', async () => {
    net.definitionFails = true;
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      const onRolledBack = mount();
      fireEvent.click((await screen.findAllByRole('button', { name: /roll back/i }))[1]);
      expect(await screen.findByTestId('version-unresolved-unchecked')).toBeTruthy();
      expect(screen.queryByTestId('version-unresolved-warning')).toBeNull();
      confirmIn(screen.getByRole('dialog'));
      await waitFor(() => expect(onRolledBack).toHaveBeenCalledTimes(1));
    } finally {
      quiet.mockRestore();
    }
  });

  it('says the check could not run before re-serving an unreadable version, and goes ahead on confirm', async () => {
    net.definitionFails = true;
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      mount();
      fireEvent.click(await screen.findByRole('button', { name: /make live/i }));
      expect(await screen.findByTestId('version-unresolved-unchecked')).toBeTruthy();
      expect(net.ops).not.toContain('ActivateDashboardVersion');
      confirmIn(screen.getByRole('dialog'));
      await waitFor(() => expect(net.ops).toContain('ActivateDashboardVersion'));
    } finally {
      quiet.mockRestore();
    }
  });
});
