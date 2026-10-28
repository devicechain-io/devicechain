// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The X.509 certificate credential, driven through the credentials panel it lives in: paste the
// device certificate, pick the anchor, and read back the pin the SERVER computed. Also the cap of
// three per device, the certificate details on the row, and the server's refusals as sentences.
// Only the transport, the toast and the confirm dialog are faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

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

import { DeviceCredentialsPanel } from './DeviceCredentialsPanel';

const CERT = '-----BEGIN CERTIFICATE-----\nMIIBdevice\n-----END CERTIFICATE-----';
const PIN = '9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08';

const ANCHOR = {
  id: 'ta-1',
  token: 'acme-ca',
  name: 'Acme device CA',
  description: null,
  certificatePem: CERT,
  fingerprintSha256: 'ab12',
  subject: 'CN=Acme',
  issuer: 'CN=Acme',
  notBefore: '2026-01-01T00:00:00Z',
  notAfter: '2036-01-01T00:00:00Z',
  enabled: true,
  credentialCount: 0,
  profileCount: 0,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
};

const certRow = (n: number, extra: object = {}) => ({
  id: String(n),
  token: `cred-x509-${n}`,
  credentialType: 'X509_CERTIFICATE',
  credentialId: `${PIN.slice(0, 60)}${String(n).padStart(4, '0')}`,
  enabled: true,
  expiresAt: '2036-01-01T00:00:00Z',
  createdAt: '2026-01-01T00:00:00Z',
  certificate: {
    fingerprintSha256: `${PIN.slice(0, 60)}${String(n).padStart(4, '0')}`,
    subjectCn: `sensor-00${n}`,
    serialHex: '01',
    notAfter: '2036-01-01T00:00:00Z',
    trustAnchor: { token: 'acme-ca', name: 'Acme device CA', enabled: true },
  },
  ...extra,
});

let rows: unknown[] = [];
let anchors: unknown[] = [];
let createError: Error | null = null;

function route() {
  gqlMock.mockImplementation((_s: string, doc: string) => {
    const d = String(doc);
    if (d.includes('mutation CreateDeviceCredential')) {
      if (createError) return Promise.reject(createError);
      return Promise.resolve({
        createDeviceCredential: {
          id: '9',
          token: 't',
          credentialType: 'X509_CERTIFICATE',
          credentialId: PIN,
          enabled: true,
          expiresAt: '2031-05-06T00:00:00Z',
          certificate: { fingerprintSha256: PIN, subjectCn: 'sensor-007', notAfter: '2031-05-06T00:00:00Z' },
        },
      });
    }
    if (d.includes('query DeviceTrustAnchors')) {
      return Promise.resolve({
        deviceTrustAnchors: { results: anchors, pagination: { pageStart: 1, pageEnd: 1, totalRecords: anchors.length } },
      });
    }
    return Promise.resolve({ deviceCredentials: { results: rows, pagination: { totalRecords: rows.length } } });
  });
}

const callsTo = (name: string) => gqlMock.mock.calls.filter((c) => String(c[1]).includes(name));

function renderPanel() {
  return render(
    <MemoryRouter>
      <DeviceCredentialsPanel deviceToken="dev-1" />
    </MemoryRouter>,
  );
}

async function chooseCertificateType() {
  fireEvent.click(screen.getByRole('button', { name: 'ACCESS_TOKEN' }));
  // Rows of this type also carry the name as a badge; the open list renders last, in a portal.
  const matches = await screen.findAllByText('X509_CERTIFICATE');
  fireEvent.click(matches[matches.length - 1]);
}

const addButton = () => screen.getByRole('button', { name: 'Add certificate credential' }) as HTMLButtonElement;

// The anchor list loads on its own; the picker is disabled until it has.
async function openAnchorPicker() {
  const button = await waitFor(() => {
    const b = document.getElementById('x509-anchor') as HTMLButtonElement;
    expect(b.disabled).toBe(false);
    return b;
  });
  fireEvent.click(button);
}

async function pickAnchor() {
  await openAnchorPicker();
  fireEvent.click(await screen.findByText('Acme device CA'));
}

beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  confirmMock.mockReset();
  rows = [];
  anchors = [ANCHOR];
  createError = null;
  route();
});
afterEach(cleanup);

describe('adding a certificate credential', () => {
  it('sends the PEM and the anchor with no credential id, and shows the pin the server computed', async () => {
    renderPanel();
    await chooseCertificateType();
    fireEvent.change(screen.getByLabelText('Device certificate (PEM)'), { target: { value: CERT } });
    await pickAnchor();
    await waitFor(() => expect(addButton().disabled).toBe(false));
    fireEvent.click(addButton());

    expect((await screen.findByTestId('pinned-fingerprint')).textContent).toBe(PIN);
    expect(screen.getByText('Common name: sensor-007')).toBeTruthy();
    const request = (callsTo('CreateDeviceCredential')[0][2] as { request: Record<string, unknown> }).request;
    expect(request).toMatchObject({
      credentialType: 'X509_CERTIFICATE',
      certificatePem: CERT,
      trustAnchorToken: 'acme-ca',
      enabled: true,
    });
    // The id is computed from the certificate; sending one is refused by the server.
    expect(request.credentialId).toBe('');
    expect(request.credentialValue).toBeUndefined();
  });

  it('offers only anchors that can vouch right now', async () => {
    anchors = [
      ANCHOR,
      { ...ANCHOR, id: 'ta-2', token: 'old-ca', name: 'Withdrawn CA', enabled: false },
      { ...ANCHOR, id: 'ta-3', token: 'dead-ca', name: 'Expired CA', notAfter: '2020-01-01T00:00:00Z' },
    ];
    renderPanel();
    await chooseCertificateType();
    await openAnchorPicker();
    expect(await screen.findByText('Acme device CA')).toBeTruthy();
    expect(screen.queryByText('Withdrawn CA')).toBeNull();
    expect(screen.queryByText('Expired CA')).toBeNull();
  });

  it('refuses a pasted private key in the browser and keeps the button off', async () => {
    renderPanel();
    await chooseCertificateType();
    fireEvent.change(screen.getByLabelText('Device certificate (PEM)'), {
      target: { value: '-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----' },
    });
    await pickAnchor();
    expect(await screen.findByText(/contains a private key/)).toBeTruthy();
    expect(addButton().disabled).toBe(true);
    expect(callsTo('CreateDeviceCredential')).toHaveLength(0);
  });

  it("shows the server's refusal of a chain that cannot authenticate, with its reason", async () => {
    createError = new GraphQLRequestError('certificate chain does not reach trust anchor "acme-ca"', 200, [
      { message: 'certificate chain does not reach trust anchor "acme-ca"', extensions: { code: 'INVALID_VALUE' } },
    ]);
    renderPanel();
    await chooseCertificateType();
    fireEvent.change(screen.getByLabelText('Device certificate (PEM)'), { target: { value: CERT } });
    await pickAnchor();
    await waitFor(() => expect(addButton().disabled).toBe(false));
    fireEvent.click(addButton());
    expect(await screen.findByText(/does not reach trust anchor "acme-ca"/)).toBeTruthy();
    expect(screen.queryByTestId('pinned-fingerprint')).toBeNull();
  });

  it('points at trust anchors when none is usable', async () => {
    anchors = [];
    renderPanel();
    await chooseCertificateType();
    expect(await screen.findByRole('link', { name: 'Manage trust anchors' })).toBeTruthy();
  });
});

describe('the cap of three certificate credentials per device', () => {
  it('blocks a fourth with an explanation, before any request', async () => {
    rows = [certRow(1), certRow(2), certRow(3)];
    renderPanel();
    await screen.findAllByText('X509_CERTIFICATE');
    await chooseCertificateType();
    expect(await screen.findByText(/already holds 3 certificate credentials/)).toBeTruthy();
    expect(addButton().disabled).toBe(true);
    expect(callsTo('CreateDeviceCredential')).toHaveLength(0);
  });

  it('does not count other credential types toward the cap', async () => {
    rows = [certRow(1), certRow(2), { ...certRow(3), credentialType: 'MQTT_BASIC', certificate: null }];
    renderPanel();
    await screen.findAllByText('X509_CERTIFICATE');
    await chooseCertificateType();
    expect(screen.queryByText(/already holds/)).toBeNull();
  });

  it('shows the server refusal if the cap is hit anyway (another operator got there first)', async () => {
    createError = new GraphQLRequestError('device "dev-1" already holds 3 certificate credentials, the most it may hold; remove one first', 200, [
      { message: 'device "dev-1" already holds 3 certificate credentials, the most it may hold; remove one first', extensions: { code: 'INVALID_VALUE' } },
    ]);
    renderPanel();
    await chooseCertificateType();
    fireEvent.change(screen.getByLabelText('Device certificate (PEM)'), { target: { value: CERT } });
    await pickAnchor();
    await waitFor(() => expect(addButton().disabled).toBe(false));
    fireEvent.click(addButton());
    expect(await screen.findByText(/already holds 3 certificate credentials, the most it may hold/)).toBeTruthy();
  });
});

describe('a certificate credential row', () => {
  it('shows the CN, the pin, the anchor and the expiry', async () => {
    rows = [certRow(1)];
    renderPanel();
    expect(await screen.findByText('CN: sensor-001')).toBeTruthy();
    expect(screen.getByText(`${PIN.slice(0, 60)}0001`)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Acme device CA' }).getAttribute('href')).toBe('/trust-anchors/acme-ca');
  });

  it('flags a withdrawn anchor and an expiry that is near or past', async () => {
    const soon = new Date(Date.now() + 5 * 86_400_000).toISOString();
    const past = new Date(Date.now() - 86_400_000).toISOString();
    rows = [
      certRow(1, {
        expiresAt: soon,
        certificate: { ...certRow(1).certificate, trustAnchor: { token: 'acme-ca', name: 'Acme device CA', enabled: false } },
      }),
      certRow(2, { expiresAt: past }),
    ];
    renderPanel();
    expect(await screen.findByText('Anchor disabled')).toBeTruthy();
    expect(screen.getByText(/Expires in [45] d/)).toBeTruthy();
    expect(screen.getByText('Expired')).toBeTruthy();
  });

  it('says so for a certificate credential that predates anchors, which cannot authenticate', async () => {
    rows = [certRow(1, { certificate: null })];
    renderPanel();
    expect(await screen.findByText(/with no trust anchor/)).toBeTruthy();
  });
});
