// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Moving straight from one registry entity to another on the same detail route. The route
// element stays mounted, and useQuery keeps the previous entity while the next one loads, so
// a tab that seeds a draft from the entity it is first handed (facet values, forms) kept the
// FIRST entity's draft — and a Save wrote it to the second. The detail page is keyed by the
// route token so a different entity remounts every tab.
import '@/i18n/config';
import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { tenant: 'oem', username: 'ann', roles: [], authorities: ['*'], typ: 'access' } }),
}));

import { ResourceDetailPage } from './ResourceDetailPage';
import type { RegistryResource } from './types';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

interface Thing {
  token: string;
  value: string;
}

const STORE: Record<string, Thing> = {
  alpha: { token: 'alpha', value: 'alpha-value' },
  bravo: { token: 'bravo', value: 'bravo-value' },
};

// A tab that behaves like the facet-values panel: a draft seeded once from the entity it
// is handed, saved against that entity's token.
function DraftTab({ item, save }: { item: Thing; save: (token: string, value: string) => void }) {
  const [draft, setDraft] = useState(item.value);
  return (
    <div>
      <input aria-label="draft" value={draft} onChange={(e) => setDraft(e.target.value)} />
      <button type="button" onClick={() => save(item.token, draft)}>
        save-draft
      </button>
    </div>
  );
}

function resourceWith(save: (token: string, value: string) => void): RegistryResource<Thing> {
  return {
    basePath: '/things',
    i18nKey: 'thing',
    list: () => Promise.resolve({ results: [], pagination: { pageStart: null, pageEnd: null, totalRecords: 0 } }),
    load: (token) => Promise.resolve(STORE[token] ?? null),
    remove: () => Promise.resolve(null),
    idOf: (t) => t.token,
    tokenOf: (t) => t.token,
    columns: [],
    nameOf: (t) => `name-${t.token}`,
    renderForm: () => null,
    detailTabs: [{ value: 'draft', label: 'draft-tab', render: (item) => <DraftTab item={item} save={save} /> }],
  };
}

function GoTo({ to }: { to: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(to)}>
      go-{to}
    </button>
  );
}

afterEach(cleanup);

describe('ResourceDetailPage across a direct entity-to-entity navigation', () => {
  it('a tab draft is seeded from the NEW entity, and its save writes the new entity’s values', async () => {
    const save = vi.fn();
    const resource = resourceWith(save);
    render(
      <MemoryRouter initialEntries={['/things/alpha']}>
        <ConfirmProvider>
          <GoTo to="/things/bravo" />
          <Routes>
            <Route path="/things/:token" element={<ResourceDetailPage resource={resource} />} />
          </Routes>
        </ConfirmProvider>
      </MemoryRouter>,
    );

    await screen.findByText('name-alpha');
    fireEvent.mouseDown(screen.getByRole('tab', { name: 'draft-tab' }));
    expect(((await screen.findByLabelText('draft')) as HTMLInputElement).value).toBe('alpha-value');

    fireEvent.click(screen.getByRole('button', { name: 'go-/things/bravo' }));
    await screen.findByText('name-bravo');
    const tab = screen.getByRole('tab', { name: 'draft-tab' });
    if (tab.getAttribute('aria-selected') !== 'true') fireEvent.mouseDown(tab);

    const input = (await screen.findByLabelText('draft')) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe('bravo-value'));
    fireEvent.click(screen.getByRole('button', { name: 'save-draft' }));
    expect(save).toHaveBeenCalledWith('bravo', 'bravo-value');
    expect(save).not.toHaveBeenCalledWith('bravo', 'alpha-value');
  });
});
