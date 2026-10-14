// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { DashboardHub, HubConnectionState } from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { ConnectionStatus } from './ConnectionStatus';
import i18n from './i18n/config';

afterEach(cleanup);

function fakeHub() {
  const listeners = new Set<() => void>();
  const hub = {
    connectionState: 'live' as HubConnectionState,
    onConnectionState(l: () => void) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
  };
  return {
    hub: hub as unknown as DashboardHub,
    set(s: HubConnectionState) {
      hub.connectionState = s;
      listeners.forEach((l) => l());
    },
  };
}

describe('/dash header connection badge', () => {
  it('renders live, reconnecting and offline as the hub moves through them', () => {
    const f = fakeHub();
    render(<ConnectionStatus hub={f.hub} />);
    expect(screen.getByRole('status').textContent).toBe('Live');
    act(() => f.set('reconnecting'));
    expect(screen.getByRole('status').textContent).toBe('Reconnecting');
    act(() => f.set('offline'));
    expect(screen.getByRole('status').textContent).toBe('Offline');
  });

  it('speaks the viewer language', async () => {
    await i18n.changeLanguage('es');
    const f = fakeHub();
    render(<ConnectionStatus hub={f.hub} />);
    expect(screen.getByRole('status').textContent).toBe('En vivo');
    act(() => f.set('offline'));
    expect(screen.getByRole('status').textContent).toBe('Sin conexión');
    await i18n.changeLanguage('en');
  });
});
