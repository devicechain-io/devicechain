// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. The GraphQL transport and the toast are
// faked; the panel, its API module and the diff run for real.
//
// What these pin: a restore or copy is PREVIEWED key by key from the documents the
// operator can see, and nothing is sent until Apply — and then exactly the mutation and
// arguments the preview described.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { DeviceConfigurationPanel } from './DeviceConfigurationPanel';
import { diffConfigurationDocuments } from './configurationDiff';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

const DIGEST = 'sha256:' + 'a'.repeat(64);

const REVISIONS = [
  { revision: 2, document: '{"interval":60}' },
  { revision: 1, document: '{"interval":30,"mode":"eco"}' },
];

function route(calls: { doc: string; vars: unknown }[]) {
  return (_svc: string, doc: string, vars: unknown) => {
    const d = String(doc);
    calls.push({ doc: d, vars });
    if (d.includes('mutation RestoreDeviceConfiguration')) {
      return Promise.resolve({ restoreDeviceConfiguration: { desired: { revision: 3 } } });
    }
    if (d.includes('mutation CopyDeviceConfiguration')) {
      return Promise.resolve({ copyDeviceConfiguration: { desired: { revision: 3 } } });
    }
    if (d.includes('query DeviceConfigurationRevisions')) {
      return Promise.resolve({
        deviceConfigurationRevisions: {
          results: REVISIONS.map((r) => ({ ...r, digest: DIGEST, createdAt: null, actor: 'op' })),
          pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
        },
      });
    }
    if (d.includes('query DeviceConfigurationDeclaration')) {
      return Promise.resolve({ devicesByToken: [] });
    }
    if (d.includes('query Devices(')) {
      return Promise.resolve({
        devices: {
          results: [{ id: '2', token: 'twin', name: 'Twin', enabled: true }],
          pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
        },
      });
    }
    // query DeviceConfiguration: the device on screen holds revision 2; "twin" holds
    // a document with a key this device does not have yet.
    const token = (vars as { deviceToken?: string })?.deviceToken;
    const document = token === 'twin' ? '{"interval":45,"mode":"boost"}' : '{"interval":60}';
    return Promise.resolve({
      deviceConfiguration: {
        desired: { revision: token === 'twin' ? 7 : 2, digest: DIGEST, document, createdAt: null, actor: '' },
        reported: null,
        pending: true,
        stale: false,
        undeclared: [],
        invalid: [],
      },
    });
  };
}

describe('diffConfigurationDocuments', () => {
  it('lists every key of either side, sorted, with its change', () => {
    expect(diffConfigurationDocuments('{"a":1,"b":"x","c":true}', '{"a":1,"b":"y","d":[1]}')).toEqual([
      { key: 'a', kind: 'unchanged', before: '1', after: '1' },
      { key: 'b', kind: 'changed', before: '"x"', after: '"y"' },
      { key: 'c', kind: 'removed', before: 'true', after: null },
      { key: 'd', kind: 'added', before: null, after: '[1]' },
    ]);
    expect(diffConfigurationDocuments(null, '{"a":1}')).toEqual([
      { key: 'a', kind: 'added', before: null, after: '1' },
    ]);
  });

  it('compares and shows a value by its exact text, so a LONG past 2^53 is not rounded', () => {
    // Both parse to the same double; they are different LONGs.
    expect(diffConfigurationDocuments('{"n":9007199254740992}', '{"n":9007199254740993}')).toEqual([
      { key: 'n', kind: 'changed', before: '9007199254740992', after: '9007199254740993' },
    ]);
  });

  it('reads nested values and strings holding delimiters whole', () => {
    expect(
      diffConfigurationDocuments('{"j":{"x":[1,{"y":"}],"}]},"s":"a,\\"b}"}', '{"j":{"x":[1,{"y":"}],"}]},"s":"a,\\"b}"}'),
    ).toEqual([
      { key: 'j', kind: 'unchanged', before: '{"x":[1,{"y":"}],"}]}', after: '{"x":[1,{"y":"}],"}]}' },
      { key: 's', kind: 'unchanged', before: '"a,\\"b}"', after: '"a,\\"b}"' },
    ]);
  });
});

describe('DeviceConfigurationPanel restore and copy', () => {
  it('offers no actions without device:write', async () => {
    gqlMock.mockImplementation(route([]));
    render(<DeviceConfigurationPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(screen.getByText('Revision history')).toBeTruthy());
    expect(screen.queryByRole('button', { name: /Restore/ })).toBeNull();
    expect(screen.queryByText('Copy from another device')).toBeNull();
  });

  it('previews a restore and sends it only on Apply', async () => {
    const calls: { doc: string; vars: unknown }[] = [];
    gqlMock.mockImplementation(route(calls));
    render(<DeviceConfigurationPanel deviceToken="dozer-01" canWrite />);

    // The current revision (2) offers no restore; revision 1 does.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Restore revision #1' })).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'Restore revision #2' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Restore revision #1' }));

    const preview = await screen.findByRole('region', { name: 'Configuration change preview' });
    expect(within(preview).getByText('Restore revision #1')).toBeTruthy();
    expect(within(preview).getByText('Changed')).toBeTruthy(); // interval 60 -> 30
    expect(within(preview).getByText('Added')).toBeTruthy(); // mode
    expect(calls.some((c) => c.doc.includes('mutation'))).toBe(false);

    fireEvent.click(within(preview).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Configuration applied as revision #3'));
    const sent = calls.filter((c) => c.doc.includes('mutation RestoreDeviceConfiguration'));
    expect(sent).toHaveLength(1);
    expect(sent[0].vars).toEqual({ deviceToken: 'dozer-01', revision: 1 });
  });

  it('previews a copy from the picked device and sends it only on Apply', async () => {
    const calls: { doc: string; vars: unknown }[] = [];
    gqlMock.mockImplementation(route(calls));
    render(<DeviceConfigurationPanel deviceToken="dozer-01" canWrite />);

    await waitFor(() => expect(screen.getByText('Copy from another device')).toBeTruthy());
    fireEvent.click(await screen.findByRole('button', { name: /Select device/ }));
    fireEvent.click(await screen.findByRole('button', { name: /Twin/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Preview changes' }));

    const preview = await screen.findByRole('region', { name: 'Configuration change preview' });
    expect(within(preview).getByText('Copy the configuration of “twin”')).toBeTruthy();
    expect(within(preview).getByText('"boost"')).toBeTruthy();
    expect(calls.some((c) => c.doc.includes('mutation'))).toBe(false);

    fireEvent.click(within(preview).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Configuration applied as revision #3'));
    const sent = calls.filter((c) => c.doc.includes('mutation CopyDeviceConfiguration'));
    expect(sent).toHaveLength(1);
    // The copy names the source revision that was previewed (twin's 7), so a source that
    // changes before Apply cannot make it apply something else.
    expect(sent[0].vars).toEqual({ fromDeviceToken: 'twin', toDeviceToken: 'dozer-01', fromRevision: 7 });
  });
});
