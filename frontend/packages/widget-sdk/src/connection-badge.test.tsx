// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { HubConnectionState } from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { HubConnectionBadge } from './connection-badge';
import { useHubConnectionState, type ConnectionStateSource } from './hooks';

afterEach(cleanup);

const labels = { live: 'Live', reconnecting: 'Reconnecting', offline: 'Offline' };

describe('HubConnectionBadge', () => {
  it.each(['live', 'reconnecting', 'offline'] as const)('renders the %s state', (state) => {
    render(<HubConnectionBadge state={state} labels={labels} />);
    const badge = screen.getByRole('status');
    expect(badge.getAttribute('data-connection-state')).toBe(state);
    expect(badge.textContent).toBe(labels[state]);
  });
});

describe('useHubConnectionState', () => {
  function fakeHub() {
    const listeners = new Set<() => void>();
    const hub = {
      connectionState: 'live' as HubConnectionState,
      onConnectionState(l: () => void) {
        listeners.add(l);
        return () => listeners.delete(l);
      },
    } satisfies ConnectionStateSource;
    return { hub, set: (s: HubConnectionState) => { hub.connectionState = s; listeners.forEach((l) => l()); }, listeners };
  }

  function Probe({ hub }: { hub: ConnectionStateSource | undefined }) {
    return <HubConnectionBadge state={useHubConnectionState(hub)} labels={labels} />;
  }

  it('follows the hub and unsubscribes on unmount', () => {
    const f = fakeHub();
    const { unmount } = render(<Probe hub={f.hub} />);
    expect(screen.getByRole('status').textContent).toBe('Live');
    act(() => f.set('reconnecting'));
    expect(screen.getByRole('status').textContent).toBe('Reconnecting');
    act(() => f.set('offline'));
    expect(screen.getByRole('status').textContent).toBe('Offline');
    unmount();
    expect(f.listeners.size).toBe(0);
  });

  it('reads live without a hub', () => {
    render(<Probe hub={undefined} />);
    expect(screen.getByRole('status').textContent).toBe('Live');
  });
});
