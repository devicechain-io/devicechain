// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SelectionCandidate } from '@devicechain/dashboards';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { WidgetCandidatesProvider, type WidgetCandidates } from './frame';
import { useCandidates } from './hooks';

afterEach(cleanup);

function Probe({ slot }: { slot?: string }) {
  const { candidates, loading, wired } = useCandidates(slot);
  return (
    <div
      data-testid="out"
      data-loading={String(loading)}
      data-wired={String(wired)}
      data-labels={candidates.map((c) => c.label).join(',')}
    />
  );
}

const cand = (label: string): SelectionCandidate => ({
  binding: { kind: 'device', deviceToken: label },
  label,
  selected: false,
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

const out = () => screen.getByTestId('out');

describe('useCandidates', () => {
  it('reports wired=false and no loading when no provider is in scope', () => {
    render(<Probe slot="s" />);
    expect(out().dataset.wired).toBe('false');
    expect(out().dataset.loading).toBe('false');
  });

  it('resolves the provider’s candidates for the slot', async () => {
    const provider: WidgetCandidates = async () => [cand('a'), cand('b')];
    render(
      <WidgetCandidatesProvider candidates={provider}>
        <Probe slot="s" />
      </WidgetCandidatesProvider>,
    );
    await waitFor(() => expect(out().dataset.labels).toBe('a,b'));
    expect(out().dataset.wired).toBe('true');
    expect(out().dataset.loading).toBe('false');
  });

  it('discards a slow in-flight fetch when the provider changes (generation guard)', async () => {
    const slow = deferred<SelectionCandidate[]>();
    const slowProvider: WidgetCandidates = () => slow.promise;
    const fastProvider: WidgetCandidates = async () => [cand('fresh')];

    const { rerender } = render(
      <WidgetCandidatesProvider candidates={slowProvider}>
        <Probe slot="s" />
      </WidgetCandidatesProvider>,
    );
    // Swap to a new provider (e.g. the parent binding changed) whose fetch resolves first.
    rerender(
      <WidgetCandidatesProvider candidates={fastProvider}>
        <Probe slot="s" />
      </WidgetCandidatesProvider>,
    );
    await waitFor(() => expect(out().dataset.labels).toBe('fresh'));

    // The stale provider resolves LATE — it must not overwrite the newer result.
    await act(async () => {
      slow.resolve([cand('stale')]);
      await slow.promise;
    });
    expect(out().dataset.labels).toBe('fresh');
  });
});
