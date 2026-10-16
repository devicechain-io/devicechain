// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Load step offers the dashboards the viewer may open, so a customer user never has to
// know a token. What the list holds is the SERVER's answer — for a member limited to some
// customers, only the published dashboards shared with them — and the viewer keeps only the
// published ones, because a never-published dashboard has nothing to show. Only the
// transport is faked.

import { CURRENT_SCHEMA_VERSION } from '@devicechain/dashboards';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  list: [] as Array<{ token: string; name: string | null; description: string | null; publishedVersion: number | null }>,
  total: null as number | null,
  listError: null as unknown,
  opened: [] as string[],
}));

vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: async (_area: string, doc: string, vars: Record<string, unknown>) => {
    if (String(doc).includes('dashboards(criteria')) {
      if (h.listError) throw h.listError;
      return { dashboards: { results: h.list, pagination: { totalRecords: h.total ?? h.list.length } } };
    }
    h.opened.push(String(vars.token));
    return {
      publishedDashboard: {
        definition: JSON.stringify({ schemaVersion: CURRENT_SCHEMA_VERSION, title: String(vars.token), widgets: [] }),
      },
    };
  },
}));

import { Load } from './App';
import i18n from './i18n/config';

beforeEach(async () => {
  await i18n.changeLanguage('en');
  h.list = [];
  h.total = null;
  h.listError = null;
  h.opened = [];
});
afterEach(cleanup);

describe('/dash dashboard list', () => {
  it('lists the published dashboards it is served and opens one with a click', async () => {
    h.list = [
      { token: 'acme-fleet', name: 'Acme fleet', description: 'Pumps at Acme', publishedVersion: 3 },
      { token: 'draft-only', name: 'Never published', description: null, publishedVersion: null },
    ];
    const onRender = vi.fn();
    render(<Load onRender={onRender} onSignOut={() => {}} />);

    const item = await screen.findByText('Acme fleet');
    // A dashboard that was never published has nothing a viewer can open, so it is not offered.
    expect(screen.queryByText('Never published')).toBeNull();
    fireEvent.click(item);
    await waitFor(() => expect(onRender).toHaveBeenCalledTimes(1));
    expect(h.opened).toEqual(['acme-fleet']);
  });

  it('says nothing is shared yet when the server answers an empty list, and keeps the token field', async () => {
    render(<Load onRender={() => {}} onSignOut={() => {}} />);
    expect(await screen.findByText(i18n.t('load:listEmpty'))).toBeTruthy();
    expect(screen.getByPlaceholderText(i18n.t('load:tokenPlaceholder'))).toBeTruthy();
  });

  // The counterweight: a refusal must not read as "nothing is shared with you".
  it('shows a failed list as an error, not as an empty one', async () => {
    h.listError = new GraphQLRequestError('GraphQL error', 200, [
      { message: 'forbidden: missing required authority', extensions: { code: 'FORBIDDEN' } },
    ]);
    render(<Load onRender={() => {}} onSignOut={() => {}} />);
    expect(await screen.findByText(i18n.t('load:errorDashboardForbidden'))).toBeTruthy();
    expect(screen.queryByText(i18n.t('load:listEmpty'))).toBeNull();
  });

  it('says when the tenant holds more dashboards than the list shows', async () => {
    h.list = [{ token: 'a', name: 'A', description: null, publishedVersion: 1 }];
    h.total = 500;
    render(<Load onRender={() => {}} onSignOut={() => {}} />);
    expect(await screen.findByText(i18n.t('load:listMore'))).toBeTruthy();
  });
});
