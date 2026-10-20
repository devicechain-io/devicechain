// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reference device types the command picker. useQuery's `data` is sticky across a key
// change, so a form reading it could offer the PREVIOUS device's commands for one render (the
// render between the change and the effect that starts the new load) and an operator could fire
// a physical actuation the new device's vocabulary does not contain. The Combobox stand-in
// records every render so that window is observed rather than skipped over by batching.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { vocabulary, drafts, renders } = vi.hoisted(() => ({
  vocabulary: vi.fn(),
  drafts: vi.fn(),
  renders: [] as string[][],
}));

vi.mock('@/lib/api/device-management', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/device-management')>();
  return {
    ...actual,
    listDevices: async () => ({ results: [] }),
    listDeviceGroupTargets: async () => [],
    listEntityGroupVersions: async () => [],
    getDeviceCommandVocabulary: (...a: unknown[]) => vocabulary(...a),
    listCommandDefinitionsForDevice: (...a: unknown[]) => drafts(...a),
  };
});

vi.mock('@/components/ui/combobox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/ui/combobox')>()),
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

import { CreateBatchForm } from './CreateBatchForm';

afterEach(cleanup);
beforeEach(() => {
  vocabulary.mockReset();
  drafts.mockReset();
  renders.length = 0;
  drafts.mockResolvedValue([]);
});

const published = (key: string) => ({
  constrained: true,
  commands: [{ commandKey: key, name: null, description: null, parameterSchema: null }],
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const paste = (text: string) =>
  fireEvent.change(screen.getByLabelText('Paste device tokens'), { target: { value: text } });

describe('CreateBatchForm never offers another device’s commands', () => {
  it('offers no command of the previous reference device while the next one loads', async () => {
    const next = deferred<ReturnType<typeof published>>();
    vocabulary.mockImplementation((token: string) =>
      token === 'dev-a' ? Promise.resolve(published('reboot-a')) : next.promise,
    );

    render(<CreateBatchForm onFired={vi.fn()} />);
    paste('dev-a');
    await screen.findByText('reboot-a');

    const before = renders.length;
    paste('dev-b');
    await waitFor(() => expect(vocabulary).toHaveBeenCalledWith('dev-b'));

    expect(screen.queryByText('reboot-a')).toBeNull();
    expect(renders.slice(before).flat()).not.toContain('reboot-a');

    next.resolve(published('reboot-b'));
    await screen.findByText('reboot-b');
    expect(renders.slice(before).flat()).not.toContain('reboot-a');
  });

  it('offers no command of the previous reference device when the next lookup fails', async () => {
    vocabulary.mockImplementation((token: string) =>
      token === 'dev-a' ? Promise.resolve(published('reboot-a')) : Promise.reject(new Error('refused')),
    );

    render(<CreateBatchForm onFired={vi.fn()} />);
    paste('dev-a');
    await screen.findByText('reboot-a');

    const before = renders.length;
    paste('dev-b');
    await waitFor(() => expect(vocabulary).toHaveBeenCalledWith('dev-b'));
    await new Promise((r) => setTimeout(r, 0));

    expect(screen.queryByText('reboot-a')).toBeNull();
    expect(renders.slice(before).flat()).not.toContain('reboot-a');
  });
});
