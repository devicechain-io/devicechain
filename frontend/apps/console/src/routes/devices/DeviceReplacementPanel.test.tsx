// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom. The real i18n catalogs are wired in by importing the config for
// its side effect, so every assertion reads the string an operator actually sees
// rather than a translation key.
//
// Three seams are faked and nothing else: the GraphQL transport, the toast, and the
// confirm dialog. The panel's real submit path, its real request shaping and the
// real API module all run.
//
// 🔴 WHY THIS FILE EXISTS. The credential minted for the incoming unit is readable
// EXACTLY ONCE — the journal stores the credential's entity token, not its id, so
// nothing can re-fetch it afterwards. A panel that forgot to render it, or that
// cleared it on the reload that follows the mutation, would leave the operator
// holding a device they cannot program, and every other assertion about the
// replacement would still pass. Nothing else in the estate notices that.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, confirmMock } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  confirmMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import { DeviceReplacementPanel } from './DeviceReplacementPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
  confirmMock.mockResolvedValue(true);
});

const BEARER = 'a1b2c3d4-minted-for-the-new-unit';

// An empty journal, which is every device's starting state.
const emptyJournal = {
  deviceReplacements: {
    results: [],
    pagination: { pageStart: 0, pageEnd: 0, totalRecords: 0 },
  },
};

const replaceResult = {
  replaceDevice: {
    device: { id: '1', token: 'dozer-01' },
    replacement: {
      id: '7',
      occurredTime: '2026-03-04T15:04:05Z',
      actor: 'tech@acme.example',
      reason: 'Water ingress',
      unitIdentifier: 'SN-88213',
    },
    newCredential: {
      id: '9',
      token: 'cred-token',
      credentialType: 'ACCESS_TOKEN',
      credentialId: BEARER,
    },
    retiredCredentialTokens: ['cred-old'],
  },
};

// Route each call by which operation it is, so the order of the panel's queries is
// not baked into the fixture.
function routeGql(replace: unknown = replaceResult, journal: unknown = emptyJournal) {
  gqlMock.mockImplementation((_service: string, doc: string) =>
    Promise.resolve(String(doc).includes('mutation ReplaceDevice') ? replace : journal),
  );
}

describe('DeviceReplacementPanel', () => {
  it('shows the minted credential once the swap lands, and keeps it visible', async () => {
    routeGql();
    render(<DeviceReplacementPanel deviceToken="dozer-01" />);

    await waitFor(() => expect(screen.getByText('This device has never been replaced.')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /Replace unit/ }));

    // The value the incoming unit is programmed with. If it is not on screen here it
    // is gone for good — the history below stores the credential's entity token, not
    // its id.
    await waitFor(() => expect(screen.getByText(BEARER)).toBeTruthy());
    expect(screen.getByText('Credential for the new unit')).toBeTruthy();

    // It survives the history reload the mutation triggers. Clearing it there would
    // be the same defect as never rendering it, one tick later.
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Unit replaced'));
    expect(screen.getByText(BEARER)).toBeTruthy();
  });

  it('sends the operator annotations and no identity fields at all', async () => {
    routeGql();
    render(<DeviceReplacementPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.change(screen.getByPlaceholderText('Water ingress'), {
      target: { value: 'Water ingress' },
    });
    fireEvent.change(screen.getByPlaceholderText('SN-88213'), { target: { value: 'SN-88213' } });
    fireEvent.click(screen.getByRole('button', { name: /Replace unit/ }));

    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Unit replaced'));
    const call = gqlMock.mock.calls.find(([, doc]) => String(doc).includes('mutation ReplaceDevice'));
    expect(call).toBeTruthy();
    const request = (call![2] as { request: Record<string, unknown> }).request;
    expect(request).toEqual({
      deviceToken: 'dozer-01',
      reason: 'Water ingress',
      unitIdentifier: 'SN-88213',
    });
    // The identity is what a replacement exists to PRESERVE, so the request must
    // carry no field that could move it. Asserted by exact equality above and named
    // here so a future addition has to argue for itself.
    for (const forbidden of ['token', 'externalId', 'deviceTypeToken', 'name']) {
      expect(Object.keys(request)).not.toContain(forbidden);
    }
  });

  it('does nothing when the operator cancels the confirmation', async () => {
    confirmMock.mockResolvedValue(false);
    routeGql();
    render(<DeviceReplacementPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('button', { name: /Replace unit/ }));

    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    expect(
      gqlMock.mock.calls.some(([, doc]) => String(doc).includes('mutation ReplaceDevice')),
    ).toBe(false);
    // The counterweight to the confirm test above: a dialog nobody can decline would
    // also pass "it replaced the device when confirmed".
    expect(screen.queryByText(BEARER)).toBeNull();
  });

  it('renders past swaps from the journal', async () => {
    routeGql(replaceResult, {
      deviceReplacements: {
        results: [
          {
            id: '7',
            occurredTime: '2026-03-04T15:04:05Z',
            actor: 'tech@acme.example',
            reason: 'Water ingress',
            unitIdentifier: 'SN-88213',
            retiredCredentialTokens: ['cred-old', 'cred-older'],
            newCredentialToken: 'cred-new',
            newCredentialType: 'ACCESS_TOKEN',
            device: { id: '1', token: 'dozer-01' },
          },
        ],
        pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
      },
    });
    render(<DeviceReplacementPanel deviceToken="dozer-01" />);

    await waitFor(() => expect(screen.getByText('tech@acme.example')).toBeTruthy());
    expect(screen.getByText('SN-88213')).toBeTruthy();
    // The count of retired credentials, not the tokens — a list of opaque uuids in a
    // table cell is noise, and the number is what an operator reads.
    expect(screen.getByText('2')).toBeTruthy();
    // The journal must NOT be a place a bearer can be read out of: it stores entity
    // tokens, and this asserts the panel does not somehow surface one anyway.
    expect(screen.queryByText(BEARER)).toBeNull();
  });

  it('reports a refusal instead of claiming the swap happened', async () => {
    gqlMock.mockImplementation((_service: string, doc: string) =>
      String(doc).includes('mutation ReplaceDevice')
        ? Promise.reject(new Error('device credential lookup ambiguous'))
        : Promise.resolve(emptyJournal),
    );
    render(<DeviceReplacementPanel deviceToken="dozer-01" />);
    await waitFor(() => expect(gqlMock).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('button', { name: /Replace unit/ }));

    await waitFor(() =>
      expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('ambiguous'), 'error'),
    );
    expect(toastMock).not.toHaveBeenCalledWith('Unit replaced');
    expect(screen.queryByText('Credential for the new unit')).toBeNull();
  });
});
