// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The artifact detail page: real i18n catalogs, real API module and documents, the real
// confirm dialog and the real authority gate. Faked: the GraphQL transport, the claims and
// the area-status read.
//
// The refusals matter more than the successes here. The service is the authority on whether
// a signature verifies, so a refusal must reach the operator in the service's own words, and
// a form that cannot tell WHICH signer was meant must not send anything at all.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, authorities, area, toastMock } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  authorities: { value: [] as string[] },
  area: { status: 'available' as 'available' | 'absent' | 'unknown' },
  toastMock: vi.fn(),
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.value } }),
}));
vi.mock('@/lib/capabilities', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/capabilities')>();
  return { ...actual, useAreaStatus: () => area.status };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import ArtifactDetailPage from './ArtifactDetailPage';

const DIGEST = 'sha256:' + 'ab12cd34ef56'.padEnd(64, '0');
const KEY_ID = 'f'.repeat(64);
const REVOKED_KEY_ID = 'e'.repeat(64);

function artifact(over: Record<string, unknown> = {}) {
  return {
    id: 'id-1',
    createdAt: '2026-10-01T10:00:00Z',
    updatedAt: '2026-10-01T10:05:00Z',
    token: 'fw-1-2-0',
    kind: 'FIRMWARE',
    name: 'sensor-fw',
    version: '1.2.0',
    component: 'firmware',
    sizeBytes: '2048',
    digest: DIGEST,
    mediaType: 'application/octet-stream',
    compatibleProfiles: ['edge'],
    trustStatus: 'UNVERIFIED',
    provenance: '{"source":"ci-build-42"}',
    state: 'AVAILABLE',
    signatures: [],
    ...over,
  };
}

function signature(over: Record<string, unknown> = {}) {
  return {
    id: 'sig-1',
    createdAt: '2026-10-02T09:30:00Z',
    createdBy: 'alice',
    countsTowardTrust: true,
    signer: {
      id: 's1',
      token: 'release-key',
      name: 'Release key',
      keyId: KEY_ID,
      algorithm: 'ED25519',
      state: 'ACTIVE',
    },
    ...over,
  };
}

function revokedSignature() {
  return signature({
    id: 'sig-2',
    createdBy: 'bob',
    countsTowardTrust: false,
    signer: {
      id: 's2',
      token: 'old-key',
      name: 'Old key',
      keyId: REVOKED_KEY_ID,
      state: 'REVOKED',
      algorithm: 'ED25519',
    },
  });
}

function signer(over: Record<string, unknown> = {}) {
  return {
    id: 's1',
    createdAt: '2026-09-01T00:00:00Z',
    token: 'release-key',
    name: 'Release key',
    algorithm: 'ED25519',
    publicKeyPem: '-----BEGIN PUBLIC KEY-----',
    keyId: KEY_ID,
    state: 'ACTIVE',
    revokedAt: null,
    createdBy: 'ops',
    revokedBy: null,
    soleSignedArtifactCount: 0,
    ...over,
  };
}

// Mutable per-test wiring.
const world = {
  artifact: artifact() as Record<string, unknown> | null,
  artifactError: null as Error | null,
  signers: [signer(), signer({ id: 's2', token: 'old-key', name: 'Old key', keyId: REVOKED_KEY_ID, state: 'REVOKED' })],
  signersError: null as Error | null,
  info: { area: 'update-management', allowUnverifiedArtifacts: false, maxArtifactBytes: '1048576' } as
    | { area: string; allowUnverifiedArtifacts: boolean; maxArtifactBytes: string }
    | null,
  retireError: null as Error | null,
  signError: null as Error | null,
  retired: [] as unknown[],
  signed: [] as unknown[],
};

beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authorities.value = ['artifact:read'];
  area.status = 'available';
  world.artifact = artifact();
  world.artifactError = null;
  world.signers = [
    signer(),
    signer({ id: 's2', token: 'old-key', name: 'Old key', keyId: REVOKED_KEY_ID, state: 'REVOKED' }),
  ];
  world.signersError = null;
  world.info = { area: 'update-management', allowUnverifiedArtifacts: false, maxArtifactBytes: '1048576' };
  world.retireError = null;
  world.signError = null;
  world.retired = [];
  world.signed = [];

  gqlMock.mockImplementation((_svc: string, document: unknown, variables: Record<string, unknown>) => {
    const doc = String(document);
    if (doc.includes('mutation RetireArtifact')) {
      world.retired.push(variables);
      if (world.retireError) return Promise.reject(world.retireError);
      world.artifact = { ...world.artifact, state: 'RETIRED' };
      return Promise.resolve({ retireArtifact: world.artifact });
    }
    if (doc.includes('mutation AddArtifactSignature')) {
      world.signed.push(variables);
      if (world.signError) return Promise.reject(world.signError);
      world.artifact = {
        ...world.artifact,
        trustStatus: 'SIGNED',
        signatures: [...((world.artifact?.signatures as unknown[]) ?? []), signature({ id: 'sig-new', createdBy: 'carol' })],
      };
      return Promise.resolve({ addArtifactSignature: world.artifact });
    }
    if (doc.includes('query ArtifactSigners')) {
      if (world.signersError) return Promise.reject(world.signersError);
      return Promise.resolve({
        artifactSigners: {
          results: world.signers,
          pagination: { pageStart: 1, pageEnd: world.signers.length, totalRecords: world.signers.length },
        },
      });
    }
    if (doc.includes('query UpdateManagementInfo')) {
      return world.info
        ? Promise.resolve({ updateManagementInfo: world.info })
        : Promise.reject(new Error('info down'));
    }
    if (doc.includes('query Artifact(')) {
      if (world.artifactError) return Promise.reject(world.artifactError);
      return Promise.resolve({ artifact: world.artifact });
    }
    throw new Error(`unexpected operation: ${doc.slice(0, 80)}`);
  });
});
afterEach(cleanup);

function renderPage() {
  render(
    <ConfirmProvider>
      <MemoryRouter initialEntries={['/updates/artifacts/fw-1-2-0']}>
        <Routes>
          <Route path="/updates/artifacts/:token" element={<ArtifactDetailPage />} />
        </Routes>
      </MemoryRouter>
    </ConfirmProvider>,
  );
}

describe('ArtifactDetailPage states', () => {
  it('shows a loading state first', () => {
    gqlMock.mockImplementation(() => new Promise(() => {}));
    renderPage();
    expect(screen.getByText('Loading artifact…')).toBeTruthy();
  });

  it('shows the error when the read fails', async () => {
    world.artifactError = new Error('boom');
    renderPage();
    expect(await screen.findByText('Failed to load data.')).toBeTruthy();
  });

  it('says so when there is no such artifact', async () => {
    world.artifact = null;
    renderPage();
    expect(await screen.findByText('No artifact with token fw-1-2-0.')).toBeTruthy();
  });

  it('shows the artifact fields and provenance', async () => {
    renderPage();
    expect(await screen.findByText('sensor-fw 1.2.0')).toBeTruthy();
    expect(screen.getByText('firmware')).toBeTruthy();
    expect(screen.getByText('2.0 KiB')).toBeTruthy();
    expect(screen.getByText('edge')).toBeTruthy();
    expect(screen.getByText('ci-build-42')).toBeTruthy();
  });
});

describe('ArtifactDetailPage trust and the unverified notice', () => {
  it('explains that an unverified artifact cannot be assigned, and links the signing guide', async () => {
    renderPage();
    const notice = await screen.findByTestId('unverified-notice');
    expect(notice.textContent).toMatch(/cannot be assigned to a device until it is signed/);
    const link = notice.querySelector('a')!;
    expect(link.getAttribute('href')).toBe('https://docs.devicechain.io/guides/signing-updates');
  });

  it('does not claim the artifact is blocked when the instance allows unverified artifacts', async () => {
    world.info = { area: 'update-management', allowUnverifiedArtifacts: true, maxArtifactBytes: '1' };
    renderPage();
    const notice = await screen.findByTestId('unverified-notice');
    expect(notice.textContent).toMatch(/allows unverified artifacts to be assigned/);
    expect(notice.textContent).not.toMatch(/cannot be assigned to a device until it is signed/);
  });

  it('does not state a rule it could not read', async () => {
    world.info = null;
    renderPage();
    const notice = await screen.findByTestId('unverified-notice');
    expect(notice.textContent).toMatch(/policy on unverified artifacts could not be read/);
  });

  it('shows no notice for a signed artifact', async () => {
    world.artifact = artifact({ trustStatus: 'SIGNED' });
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    expect(screen.queryByTestId('unverified-notice')).toBeNull();
  });

  it("lists the artifact's signatures and says which still count toward trust", async () => {
    world.artifact = artifact({ trustStatus: 'SIGNED', signatures: [signature(), revokedSignature()] });
    renderPage();
    const rows = await screen.findAllByTestId('signature-row');
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain('Release key');
    expect(rows[0].textContent).toContain('alice');
    expect(rows[0].textContent).toContain('Active');
    expect(rows[0].textContent).toContain('Counts toward trust');
    expect(rows[0].textContent).not.toContain('No longer counts');
    expect(rows[1].textContent).toContain('Old key');
    expect(rows[1].textContent).toContain('bob');
    expect(rows[1].textContent).toContain('Revoked');
    expect(rows[1].textContent).toContain('No longer counts');
    expect(rows[1].textContent).not.toContain('Counts toward trust');
    // The old note and the registry listing are gone.
    expect(screen.queryByText(/does not list an artifact's individual signatures/)).toBeNull();
  });

  it('says so when the artifact has no signatures, and does not list unrelated registered signers', async () => {
    renderPage();
    expect(await screen.findByText(/No signatures are recorded for this artifact/)).toBeTruthy();
    expect(screen.queryByTestId('signature-row')).toBeNull();
    expect(screen.queryByText('Release key')).toBeNull();
  });

  it('shows a field error as an error, never as an empty list', async () => {
    world.artifactError = new Error('signer for signature sig-1 could not be read');
    renderPage();
    expect(await screen.findByText('Failed to load data.')).toBeTruthy();
    expect(screen.queryByText(/No signatures are recorded/)).toBeNull();
  });

  it('flags a failed re-read instead of passing the stale list off as current', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    world.artifact = artifact({ signatures: [signature()] });
    renderPage();
    await screen.findAllByTestId('signature-row');
    world.artifactError = new Error('signer unreadable');
    world.signError = null;
    fireEvent.change(screen.getByLabelText('Signer key ID'), { target: { value: KEY_ID } });
    fireEvent.change(screen.getByLabelText('Signature (base64)'), { target: { value: 'QUJD' } });
    await screen.findByText(/Signer: Release key/);
    fireEvent.click(screen.getByRole('button', { name: /Add signature/ }));
    expect(await screen.findByTestId('signatures-refresh-error')).toBeTruthy();
  });
});

describe('ArtifactDetailPage authority', () => {
  it('hides Retire, the signature form and the upload panel from a reader', async () => {
    world.artifact = artifact({ state: 'UPLOADING' });
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    expect(screen.queryByRole('button', { name: /Retire/ })).toBeNull();
    expect(screen.queryByTestId('add-signature-form')).toBeNull();
    expect(screen.queryByText('Upload content')).toBeNull();
  });

  it('shows them to a holder of artifact:write', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    world.artifact = artifact({ state: 'UPLOADING' });
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    expect(screen.getByRole('button', { name: /Retire/ })).toBeTruthy();
    expect(screen.getByTestId('add-signature-form')).toBeTruthy();
    expect(screen.getByText('Upload content')).toBeTruthy();
  });

  it('does not offer Retire on an artifact that is already retired', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    world.artifact = artifact({ state: 'RETIRED' });
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    expect(screen.queryByRole('button', { name: /Retire/ })).toBeNull();
  });
});

describe('ArtifactDetailPage retire', () => {
  beforeEach(() => {
    authorities.value = ['artifact:read', 'artifact:write'];
  });

  it('retires only after the operator confirms', async () => {
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');

    fireEvent.click(screen.getByRole('button', { name: /^Retire$/ }));
    expect(await screen.findByText('Retire sensor-fw 1.2.0?')).toBeTruthy();
    expect(world.retired).toHaveLength(0);

    fireEvent.click(screen.getAllByRole('button', { name: 'Retire' }).pop()!);
    await waitFor(() => expect(world.retired).toEqual([{ token: 'fw-1-2-0' }]));
    // The page re-reads and shows the new lifecycle state.
    expect(await screen.findByText('Retired')).toBeTruthy();
  });

  it('does nothing when the confirmation is dismissed', async () => {
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');

    fireEvent.click(screen.getByRole('button', { name: /^Retire$/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByText('Retire sensor-fw 1.2.0?')).toBeNull());
    expect(world.retired).toHaveLength(0);
  });

  it("shows the service's refusal verbatim", async () => {
    world.retireError = new Error('ARTIFACT_IN_USE: 3 devices are assigned this artifact');
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');

    fireEvent.click(screen.getByRole('button', { name: /^Retire$/ }));
    await screen.findByText('Retire sensor-fw 1.2.0?');
    fireEvent.click(screen.getAllByRole('button', { name: 'Retire' }).pop()!);

    expect(await screen.findByText('ARTIFACT_IN_USE: 3 devices are assigned this artifact')).toBeTruthy();
  });
});

describe('ArtifactDetailPage add signature', () => {
  beforeEach(() => {
    authorities.value = ['artifact:read', 'artifact:write'];
  });

  async function openForm() {
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    await screen.findByTestId('add-signature-form');
  }
  const keyField = () => screen.getByLabelText('Signer key ID');
  const sigField = () => screen.getByLabelText('Signature (base64)');
  const submit = () => screen.getByRole('button', { name: /Add signature/ }) as HTMLButtonElement;

  it('resolves the key ID to its signer and sends that signer token with the signature', async () => {
    await openForm();
    fireEvent.change(keyField(), { target: { value: KEY_ID.toUpperCase() } });
    fireEvent.change(sigField(), { target: { value: 'QUJD\nREVG' } });
    expect(await screen.findByText(/Signer: Release key/)).toBeTruthy();

    fireEvent.click(submit());

    await waitFor(() =>
      expect(world.signed).toEqual([
        { artifactToken: 'fw-1-2-0', signerToken: 'release-key', signature: 'QUJDREVG' },
      ]),
    );
    // The new trust comes back with the mutation and is reported.
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Signature added. Trust is now Signed.'));
  });

  it('re-reads the artifact after an add so the new signature appears', async () => {
    await openForm();
    expect(screen.queryByTestId('signature-row')).toBeNull();
    const reads = () => gqlMock.mock.calls.filter((c) => String(c[1]).includes('query Artifact(')).length;
    const before = reads();
    fireEvent.change(keyField(), { target: { value: KEY_ID } });
    fireEvent.change(sigField(), { target: { value: 'QUJD' } });
    await screen.findByText(/Signer: Release key/);
    fireEvent.click(submit());
    const rows = await screen.findAllByTestId('signature-row');
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain('carol');
    expect(reads()).toBeGreaterThan(before);
  });

  it('refuses to send for a key ID no registered signer has', async () => {
    await openForm();
    fireEvent.change(keyField(), { target: { value: 'a'.repeat(64) } });
    fireEvent.change(sigField(), { target: { value: 'QUJD' } });

    expect(await screen.findByText('No registered signer has this key ID.')).toBeTruthy();
    expect(submit().disabled).toBe(true);
    expect(world.signed).toHaveLength(0);
  });

  it('refuses to send for a revoked signer', async () => {
    await openForm();
    fireEvent.change(keyField(), { target: { value: REVOKED_KEY_ID } });
    fireEvent.change(sigField(), { target: { value: 'QUJD' } });

    expect(await screen.findByText(/This signer is revoked/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });

  it('refuses a signature that is not base64 before sending', async () => {
    await openForm();
    fireEvent.change(keyField(), { target: { value: KEY_ID } });
    fireEvent.change(sigField(), { target: { value: 'not base64!' } });

    expect(await screen.findByText('This is not standard base64.')).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });

  it('fails closed when the signer registry cannot be read', async () => {
    world.signersError = new Error('down');
    renderPage();
    await screen.findByText('sensor-fw 1.2.0');
    fireEvent.change(keyField(), { target: { value: KEY_ID } });
    fireEvent.change(sigField(), { target: { value: 'QUJD' } });

    expect(await screen.findByText(/signers could not be read/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });

  it("shows the service's refusal verbatim and keeps what was typed", async () => {
    world.signError = new Error('SIGNATURE_INVALID: signature does not verify against sha256:ab12');
    await openForm();
    fireEvent.change(keyField(), { target: { value: KEY_ID } });
    fireEvent.change(sigField(), { target: { value: 'QUJD' } });
    await screen.findByText(/Signer: Release key/);

    fireEvent.click(submit());

    expect(await screen.findByText('SIGNATURE_INVALID: signature does not verify against sha256:ab12')).toBeTruthy();
    expect((sigField() as HTMLTextAreaElement).value).toBe('QUJD');
    expect(toastMock).not.toHaveBeenCalled();
  });
});

describe('ArtifactDetailPage moving between artifacts', () => {
  function GoTo({ token }: { token: string }) {
    const navigate = useNavigate();
    return <button onClick={() => navigate(`/updates/artifacts/${token}`)}>go-{token}</button>;
  }

  it("never shows the previous artifact under the next one's URL while it loads", async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    render(
      <ConfirmProvider>
        <MemoryRouter initialEntries={['/updates/artifacts/fw-1-2-0']}>
          <GoTo token="fw-9" />
          <Routes>
            <Route path="/updates/artifacts/:token" element={<ArtifactDetailPage />} />
          </Routes>
        </MemoryRouter>
      </ConfirmProvider>,
    );
    await screen.findByText('sensor-fw 1.2.0');
    // The next artifact's read never answers: whatever is on screen is the old one or nothing.
    const base = gqlMock.getMockImplementation()!;
    gqlMock.mockImplementation((svc: string, document: unknown, variables: Record<string, unknown>) =>
      String(document).includes('query Artifact(') ? new Promise(() => {}) : base(svc, document, variables),
    );
    fireEvent.click(screen.getByRole('button', { name: 'go-fw-9' }));
    await screen.findByText('Loading artifact…');
    expect(screen.queryByText('sensor-fw 1.2.0')).toBeNull();
    expect(screen.queryByRole('button', { name: /Retire/ })).toBeNull();
  });
});

describe('ArtifactDetailPage upload of an UPLOADING artifact', () => {
  it('refuses a file whose size is not the declared one before sending anything', async () => {
    authorities.value = ['artifact:read', 'artifact:write'];
    world.artifact = artifact({ state: 'UPLOADING' });
    const xhr = vi.fn();
    vi.stubGlobal('XMLHttpRequest', xhr);
    try {
      renderPage();
      await screen.findByText('Upload content');
      const input = document.querySelector('input[type=file]') as HTMLInputElement;
      fireEvent.change(input, { target: { files: [new File(['abc'], 'fw.bin')] } });
      await screen.findByText(/This file is 3 bytes; the artifact declares 2048/);
      expect(xhr).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
