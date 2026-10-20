// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useQuery's `data` is sticky across a key change. CommandEditor guards its loading and error
// states in the JSX, so the only render that could offer the PREVIOUS device's commands is the
// one between the deviceToken change and the effect that starts the new load (loading is still
// false there). The Combobox stand-in below records every render, so that window is observed
// rather than skipped over by React's batching.
import '@/i18n/config';
import type { WidgetDescriptor } from '@devicechain/dashboards';
import type { EditorCommandChoices } from '@devicechain/widget-sdk';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { renders } = vi.hoisted(() => ({ renders: [] as string[][] }));
vi.mock('@/components/ui/combobox', () => ({
  Combobox: ({ options }: { options: { value: string; label?: string }[] }) => {
    renders.push(options.map((o) => o.value));
    return (
      <ul>
        {options.map((o) => (
          <li key={o.value}>{o.label ?? o.value}</li>
        ))}
      </ul>
    );
  },
}));

import { hostControl } from './controlEditors';

afterEach(cleanup);
beforeEach(() => {
  renders.length = 0;
});

const choicesOf = (key: string): EditorCommandChoices => ({
  constrained: true,
  selectable: [{ commandKey: key, name: key, parameterSchema: '[]' }],
  draftOnly: [],
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const DESCRIPTOR = { id: 'acme/probe' } as unknown as WidgetDescriptor;

describe('CommandEditor never offers another device’s commands', () => {
  function mount(deviceToken: string, lookup: (t: string) => Promise<EditorCommandChoices>) {
    const Editor = hostControl('command-vocabulary')!.Editor!;
    return (
      <Editor
        options={{}}
        setOptions={vi.fn()}
        services={{ slots: {}, deviceToken, commandChoices: lookup }}
        descriptor={DESCRIPTOR}
        optionKeys={['commandName', 'commandLabel', 'parameterSchema']}
      />
    );
  }

  it('offers no command of the previous device from the moment the device changes', async () => {
    const next = deferred<EditorCommandChoices>();
    const lookup = vi.fn((token: string) => (token === 'dev-a' ? Promise.resolve(choicesOf('reboot-a')) : next.promise));

    const { rerender } = render(mount('dev-a', lookup));
    await screen.findByText(/reboot-a/);

    const before = renders.length;
    rerender(mount('dev-b', lookup));
    await waitFor(() => expect(lookup).toHaveBeenCalledTimes(2));

    expect(screen.queryByText(/reboot-a/)).toBeNull();
    expect(renders.slice(before).flat()).not.toContain('reboot-a');

    next.resolve(choicesOf('reboot-b'));
    await screen.findByText(/reboot-b/);
    expect(renders.slice(before).flat()).not.toContain('reboot-a');
  });

  it('offers no command of the previous device when the next lookup fails', async () => {
    const lookup = vi.fn((token: string) =>
      token === 'dev-a' ? Promise.resolve(choicesOf('reboot-a')) : Promise.reject(new Error('refused')),
    );

    const { rerender } = render(mount('dev-a', lookup));
    await screen.findByText(/reboot-a/);

    const before = renders.length;
    rerender(mount('dev-b', lookup));
    await waitFor(() => expect(lookup).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));

    expect(screen.queryByText(/reboot-a/)).toBeNull();
    expect(renders.slice(before).flat()).not.toContain('reboot-a');
  });
});
