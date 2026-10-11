// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. Only the GraphQL transport is faked;
// the panel, its API module and useQuery all run.
//
// 🔴 WHY THIS FILE EXISTS. Two things here fail quietly:
//   - `pending` is the server's verdict (same revision AND same digest AND APPLIED).
//     A panel that recomputed it from the revision numbers would show "In sync" for a
//     device that reported the right number with the wrong digest. The fixtures below
//     make the numbers and the flag disagree, so only reading the flag passes.
//   - useQuery keeps the previous result while a refetch is in flight, so moving to
//     another device would paint the previous device's configuration under the new
//     device's heading.
import '@/i18n/config';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { GraphQLRequestError } from '@devicechain/client';
import { afterEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceConfigurationPanel } from './DeviceConfigurationPanel';
import { configurationBanner } from './configurationState';

afterEach(cleanup);
// No beforeEach reset: every test installs its own implementation.

const DIGEST_A = 'sha256:' + 'a'.repeat(64);
const DIGEST_B = 'sha256:' + 'b'.repeat(64);

interface Fixture {
  desired?: { revision: number; digest: string; document: string } | null;
  reported?: {
    revision: number | null;
    digest: string | null;
    status: string | null;
    errors: string[];
  } | null;
  pending?: boolean;
  stale?: boolean;
  undeclared?: string[];
  invalid?: string[];
  declaration?: { key: string; valueType: string; description: string | null }[];
}

function cfgData(f: Fixture) {
  const desired = f.desired === undefined ? null : f.desired;
  return {
    deviceConfiguration: {
      desired: desired && { ...desired, createdAt: '2026-03-04T15:04:05Z', actor: 'op@acme.example' },
      reported: f.reported && { ...f.reported, reportedAt: null, lastSyncAt: null },
      pending: f.pending ?? false,
      stale: f.stale ?? false,
      undeclared: f.undeclared ?? [],
      invalid: f.invalid ?? [],
    },
  };
}

function route(f: Fixture, revisions: { revision: number; digest: string }[] = []) {
  return (_svc: string, doc: string) => {
    const d = String(doc);
    if (d.includes('query DeviceConfigurationRevisions')) {
      return Promise.resolve({
        deviceConfigurationRevisions: {
          results: revisions.map((r) => ({ ...r, document: '{}', createdAt: null, actor: 'op' })),
          pagination: { pageStart: 1, pageEnd: revisions.length, totalRecords: revisions.length },
        },
      });
    }
    if (d.includes('query DeviceConfigurationDeclaration')) {
      return Promise.resolve({
        devicesByToken: [
          { token: 'x', deviceType: { token: 't', profile: { token: 'p', activeProfileVersion: { version: 1, configurationDeclaration: f.declaration ?? [] } } } },
        ],
      });
    }
    return Promise.resolve(cfgData(f));
  };
}

describe('configurationBanner', () => {
  it('follows the server pending flag, not the revision numbers', () => {
    const base = {
      desired: { revision: 3, digest: DIGEST_A, document: '{}', createdAt: null, actor: '' },
      reported: { revision: 3, digest: DIGEST_B, status: 'APPLIED', errors: [], reportedAt: null, lastSyncAt: null },
      stale: false,
      undeclared: [],
      invalid: [],
    };
    // Same revision number, server says pending (digest differs).
    expect(configurationBanner({ ...base, pending: true })).toBe('pending');
    // Lower reported number, server says not pending.
    expect(
      configurationBanner({ ...base, pending: false, reported: { ...base.reported, revision: 1 } }),
    ).toBe('inSync');
    expect(configurationBanner({ ...base, desired: null, pending: false })).toBe('noRevision');
  });
});

describe('DeviceConfigurationPanel', () => {
  it('renders pending from the server field even when the revision numbers match', async () => {
    gqlMock.mockImplementation(
      route(
        {
          desired: { revision: 3, digest: DIGEST_A, document: '{"mode":"eco"}' },
          reported: { revision: 3, digest: DIGEST_B, status: 'RECEIVED', errors: [] },
          pending: true,
        },
        [{ revision: 3, digest: DIGEST_A }],
      ),
    );
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('Pending')).toBeTruthy());
    expect(screen.queryByText('In sync')).toBeNull();
    expect(screen.getByText('Received')).toBeTruthy();
    expect(screen.getByText('{"mode":"eco"}')).toBeTruthy();
  });

  it('renders in sync from the server field even when the reported revision lags', async () => {
    gqlMock.mockImplementation(
      route({
        desired: { revision: 5, digest: DIGEST_A, document: '{}' },
        reported: { revision: 2, digest: DIGEST_B, status: 'APPLIED', errors: [] },
        pending: false,
      }),
    );
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('In sync')).toBeTruthy());
    expect(screen.queryByText('Pending')).toBeNull();
  });

  it('shows rejection errors, declared keys, invalid and undeclared keys', async () => {
    gqlMock.mockImplementation(
      route({
        desired: { revision: 1, digest: DIGEST_A, document: '{}' },
        reported: { revision: 1, digest: DIGEST_A, status: 'REJECTED', errors: ['unknown mode eco2'] },
        pending: true,
        invalid: ['rate'],
        undeclared: ['internal-note'],
        declaration: [
          { key: 'mode', valueType: 'STRING', description: 'Operating mode' },
          { key: 'rate', valueType: 'LONG', description: null },
        ],
      }),
    );
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('unknown mode eco2')).toBeTruthy());
    expect(screen.getByText('Rejected')).toBeTruthy();
    expect(screen.getByText('Operating mode')).toBeTruthy();
    expect(screen.getByText('Invalid value')).toBeTruthy();
    expect(screen.getByText('internal-note')).toBeTruthy();
  });

  it('shows the empty state when no revision exists', async () => {
    gqlMock.mockImplementation(route({ desired: null, reported: null }));
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('No revision yet')).toBeTruthy());
    expect(screen.getByText('No revisions yet.')).toBeTruthy();
    expect(screen.getByText('Never reported')).toBeTruthy();
  });

  it('shows an error state when the configuration read fails', async () => {
    gqlMock.mockRejectedValue(new GraphQLRequestError('the read failed', 200));
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('the read failed')).toBeTruthy());
  });

  it('does not show the previous device while the next device loads', async () => {
    gqlMock.mockImplementation(
      route({
        desired: { revision: 1, digest: DIGEST_A, document: '{"owner":"first"}' },
        reported: null,
        pending: true,
      }),
    );
    const { rerender } = render(<DeviceConfigurationPanel deviceToken="first" />);
    await waitFor(() => expect(screen.getByText('{"owner":"first"}')).toBeTruthy());

    // The next device's reads never resolve: whatever is on screen is what the panel
    // shows for a device it has no data for.
    gqlMock.mockImplementation(() => new Promise(() => {}));
    rerender(<DeviceConfigurationPanel deviceToken="second" />);

    expect(screen.queryByText('{"owner":"first"}')).toBeNull();
    expect(screen.getByText('Loading configuration…')).toBeTruthy();
  });
});
