// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { useQuery } from '@/lib/hooks/use-query';
import { QueryBoundary } from './query-boundary';

afterEach(cleanup);

it('retries initial and refresh errors while preserving the mounted draft', async () => {
  const loader = vi.fn()
    .mockRejectedValueOnce(new Error('initial'))
    .mockResolvedValueOnce('record')
    .mockRejectedValueOnce(new Error('refresh'))
    .mockResolvedValueOnce('updated');
  function Draft() {
    const [value, setValue] = useState('draft');
    return <input aria-label="draft" value={value} onChange={(e) => setValue(e.target.value)} />;
  }
  function Page() {
    const query = useQuery<string>(loader, [], ['record']);
    return <>
      <button onClick={query.refetch}>refresh</button>
      <QueryBoundary query={query}><span>{query.data}</span><Draft /></QueryBoundary>
    </>;
  }
  render(<Page />);
  fireEvent.click(await screen.findByRole('button', { name: 'Retry' }));
  await screen.findByText('record');
  fireEvent.change(screen.getByLabelText('draft'), { target: { value: 'unsaved' } });
  fireEvent.click(screen.getByRole('button', { name: 'refresh' }));
  const retry = await screen.findByRole('button', { name: 'Retry' });
  expect((screen.getByLabelText('draft') as HTMLInputElement).value).toBe('unsaved');
  expect(screen.getByText('record')).toBeTruthy();
  fireEvent.click(retry);
  await screen.findByText('updated');
  expect((screen.getByLabelText('draft') as HTMLInputElement).value).toBe('unsaved');
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
});
