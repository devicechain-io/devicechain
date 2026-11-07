// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The server-backed picker: it asks for matches as the operator types (after a pause), keeps
// only the newest answer, names a value its current page does not hold, and says when there
// are more matches than it shows.
import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AsyncCombobox, type AsyncComboboxPage } from './async-combobox';

afterEach(cleanup);

const opt = (value: string, label = value) => ({ value, label });

function Harness(props: {
  search: (q: string) => Promise<AsyncComboboxPage>;
  resolve?: (v: string) => Promise<{ value: string; label?: string } | null>;
  initial?: string;
  onChange?: (v: string) => void;
}) {
  const [value, setValue] = useState(props.initial ?? '');
  return (
    <AsyncCombobox
      id="pick"
      value={value}
      onChange={(v) => {
        setValue(v);
        props.onChange?.(v);
      }}
      search={props.search}
      resolve={props.resolve}
    />
  );
}

const open = () => fireEvent.click(document.getElementById('pick') as HTMLButtonElement);

describe('AsyncCombobox', () => {
  it('loads the first page on open and chooses an option', async () => {
    const search = vi.fn().mockResolvedValue({ options: [opt('a', 'Alpha'), opt('b', 'Beta')] });
    const onChange = vi.fn();
    render(<Harness search={search} onChange={onChange} />);
    expect(search).not.toHaveBeenCalled();
    open();
    fireEvent.click(await screen.findByText('Beta'));
    expect(search).toHaveBeenCalledWith('');
    expect(onChange).toHaveBeenCalledWith('b');
    // The trigger names the choice even though choosing closed the list.
    expect(document.getElementById('pick')?.textContent).toContain('Beta');
  });

  it('searches the server for what is typed, once, after a pause', async () => {
    const search = vi.fn().mockResolvedValue({ options: [opt('a')] });
    render(<Harness search={search} />);
    open();
    await screen.findByText('a');
    const box = screen.getByPlaceholderText('Search…');
    fireEvent.change(box, { target: { value: 'p' } });
    fireEvent.change(box, { target: { value: 'pu' } });
    fireEvent.change(box, { target: { value: 'pum' } });
    await waitFor(() => expect(search).toHaveBeenCalledWith('pum'));
    expect(search.mock.calls.map((c) => c[0])).toEqual(['', 'pum']);
  });

  it('keeps only the newest answer when an earlier request lands late', async () => {
    let releaseSlow: (p: AsyncComboboxPage) => void = () => {};
    const search = vi.fn((q: string) =>
      q === 'slow'
        ? new Promise<AsyncComboboxPage>((r) => (releaseSlow = r))
        : Promise.resolve({ options: [opt(q === '' ? 'first' : 'fast')] }),
    );
    render(<Harness search={search} />);
    open();
    await screen.findByText('first');
    const box = screen.getByPlaceholderText('Search…');
    fireEvent.change(box, { target: { value: 'slow' } });
    await waitFor(() => expect(search).toHaveBeenCalledWith('slow'));
    fireEvent.change(box, { target: { value: 'fast' } });
    await screen.findByText('fast');
    releaseSlow({ options: [opt('stale')] });
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText('stale')).toBeNull();
    expect(screen.getByText('fast')).toBeTruthy();
  });

  it('names a value the page does not contain by resolving it', async () => {
    const resolve = vi.fn().mockResolvedValue(opt('gw', 'Gateway'));
    render(<Harness search={vi.fn().mockResolvedValue({ options: [] })} resolve={resolve} initial="gw" />);
    await waitFor(() => expect(document.getElementById('pick')?.textContent).toContain('Gateway'));
    expect(resolve).toHaveBeenCalledWith('gw');
  });

  it('falls back to the raw value when it cannot be resolved', async () => {
    const resolve = vi.fn().mockResolvedValue(null);
    render(<Harness search={vi.fn().mockResolvedValue({ options: [] })} resolve={resolve} initial="ghost" />);
    await waitFor(() => expect(resolve).toHaveBeenCalled());
    expect(document.getElementById('pick')?.textContent).toContain('ghost');
  });

  it('says how many matches it is not showing', async () => {
    render(<Harness search={vi.fn().mockResolvedValue({ options: [opt('a'), opt('b')], total: 1234 })} />);
    open();
    expect(await screen.findByText(/1232 more matches/)).toBeTruthy();
  });

  it('says so when nothing matches, and when the search fails', async () => {
    const search = vi.fn().mockResolvedValueOnce({ options: [] }).mockRejectedValueOnce(new Error('boom'));
    render(<Harness search={search} />);
    open();
    expect(await screen.findByText('No matches.')).toBeTruthy();
    fireEvent.change(screen.getByPlaceholderText('Search…'), { target: { value: 'x' } });
    expect(await screen.findByRole('alert')).toBeTruthy();
  });

  it('clears the selection', async () => {
    const onChange = vi.fn();
    render(<Harness search={vi.fn().mockResolvedValue({ options: [opt('a')] })} initial="a" onChange={onChange} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Clear' }));
    expect(onChange).toHaveBeenCalledWith('');
  });
});
