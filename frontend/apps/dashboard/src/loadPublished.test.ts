// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// /dash fetches a dashboard BY TOKEN, and the thing it fetches is the PUBLISHED
// snapshot. These tests drive the real loadPublishedDashboard with the transport
// replaced, so what is measured is which operation is sent and how each answer is
// classified -- not the transport.

/// <reference types="node" />

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { beforeEach, describe, expect, it, vi } from 'vitest';

// A plain recording function rather than vi.fn(): vitest reports a spy whose last call
// threw as a failure of the test, which is exactly what these tests provoke on purpose.
const h = vi.hoisted(() => ({
  preloaded: [] as unknown[][],
  calls: [] as unknown[][],
  impl: (async () => undefined) as (...args: unknown[]) => Promise<unknown>,
}));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: (...args: unknown[]) => {
    h.calls.push(args);
    return h.impl(...args);
  },
}));

// The widget-code preload, recorded: the published door must ask for the board's own types.
vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  preloadWidgets: async (...args: unknown[]) => {
    h.preloaded.push(args);
  },
}));

const { GraphQLRequestError } = await import('@devicechain/client');
const { loadPublishedDashboard } = await import('./load');

const board = JSON.stringify({ schemaVersion: 1, title: 'Live', widgets: [] });

beforeEach(() => {
  h.preloaded = [];
  h.calls = [];
});

const resolveWith = (value: unknown) => {
  h.impl = async () => value;
};
const rejectWith = (err: unknown) => {
  h.impl = async () => {
    throw err;
  };
};

describe('loadPublishedDashboard', () => {
  it('fetches the code of the lazily loaded widgets before handing the board over', async () => {
    const withWidgets = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards', 'sp-dashboard.json'),
      'utf8',
    );
    resolveWith({ publishedDashboard: { definition: withWidgets } });
    const result = await loadPublishedDashboard('sp', '');
    if (!('loaded' in result)) throw new Error('expected a loaded board');
    expect(h.preloaded).toHaveLength(1);
    expect(h.preloaded[0][0]).toBe(result.loaded.registry);
    expect(h.preloaded[0][1]).toEqual(result.loaded.definition.widgets.map((w) => w.type));
    expect(result.loaded.definition.widgets.length).toBeGreaterThan(1);
  });

  it('asks the dashboard-management area for the published snapshot by token', async () => {
    resolveWith({ publishedDashboard: { definition: board } });

    const result = await loadPublishedDashboard('  ops-overview  ', '');

    expect('loaded' in result && result.loaded.definition.title).toBe('Live');
    const [area, doc, variables] = h.calls[0];
    expect(area).toBe('dashboard-management');
    expect(variables).toEqual({ token: 'ops-overview' });
    // The draft is author-only: this viewer must never ask for it. The document names the
    // published door and no other.
    const text = String(doc);
    expect(text).toContain('publishedDashboard(token: $token)');
    expect(text).not.toMatch(/\bdashboard\(token/);
    expect(text).not.toContain('dashboardVersion');
  });

  it('runs the fetched document through the same parser a paste uses, manifest included', async () => {
    resolveWith({ publishedDashboard: { definition: board } });
    const dropped = await loadPublishedDashboard('ops', '{"zone":{"kind":"nonsense"}}');
    expect('error' in dropped && dropped.error).toMatchObject({ code: 'manifestDropped' });

    const bad = '{not json';
    resolveWith({ publishedDashboard: { definition: bad } });
    const invalid = await loadPublishedDashboard('ops', '');
    expect('error' in invalid && invalid.error).toMatchObject({ code: 'definitionInvalid' });
  });

  it('reports an unknown token as not found', async () => {
    resolveWith({ publishedDashboard: null });
    expect(await loadPublishedDashboard('nope', '')).toEqual({ error: { code: 'dashboardNotFound' } });
  });

  it('classifies by the server code, not by the message', async () => {
    rejectWith(
      new GraphQLRequestError('worded any way at all', 200, [
        { message: 'worded any way at all', extensions: { code: 'NOT_PUBLISHED' } },
      ]),
    );
    expect(await loadPublishedDashboard('ops', '')).toEqual({ error: { code: 'dashboardNotPublished' } });

    rejectWith(
      new GraphQLRequestError('forbidden: missing required authority', 200, [
        { message: 'forbidden: missing required authority' },
      ]),
    );
    expect(await loadPublishedDashboard('ops', '')).toEqual({ error: { code: 'dashboardForbidden' } });

    // The same words WITHOUT the code are not a NOT_PUBLISHED.
    rejectWith(
      new GraphQLRequestError('dashboard has not been published', 200, [
        { message: 'dashboard has not been published' },
      ]),
    );
    expect(await loadPublishedDashboard('ops', '')).toMatchObject({
      error: { code: 'dashboardFetchFailed' },
    });
  });

  it('returns a transport failure as a value, never a throw', async () => {
    rejectWith(new TypeError('Failed to fetch'));
    expect(await loadPublishedDashboard('ops', '')).toEqual({
      error: { code: 'dashboardFetchFailed', detail: 'Failed to fetch' },
    });
    rejectWith('not an Error at all');
    expect(await loadPublishedDashboard('ops', '')).toEqual({
      error: { code: 'dashboardFetchFailed', detail: null },
    });
  });
});
