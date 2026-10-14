// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The declare-then-upload flow. Faked: the GraphQL transport, the upload transport (the XHR
// itself is tested in lib/artifactUpload.test.ts) and the SHA-256 (jsdom's Blob has no
// arrayBuffer in every version). The kit's Combobox and MultiSelect are replaced by native
// controls: their popover mechanics are the kit's to test, and what this file asserts is
// WHAT THE FORM SENDS and WHAT IT REFUSES TO SEND.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, uploadMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), uploadMock: vi.fn() }));

const DIGEST = 'sha256:' + 'c0ffee'.padEnd(64, '1');

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/lib/artifactUpload', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/artifactUpload')>();
  return {
    ...actual,
    sha256Digest: () => Promise.resolve(DIGEST),
    uploadArtifactContent: (...args: unknown[]) => uploadMock(...args),
  };
});
vi.mock('@/components/ui/combobox', () => ({
  Combobox: ({ options, value, onChange, ariaLabel }: {
    options: { value: string; label: string }[];
    value: string;
    onChange: (v: string) => void;
    ariaLabel?: string;
  }) => (
    <select aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="" />
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  ),
}));
vi.mock('@/components/ui/multi-select', () => ({
  MultiSelect: ({ onChange }: { onChange: (v: string[]) => void }) => (
    <button type="button" onClick={() => onChange(['edge'])}>
      pick-profile
    </button>
  ),
}));

import { NewArtifactForm } from './NewArtifactForm';

const world = {
  maxBytes: '1048576' as string,
  infoFails: false,
  createError: null as Error | null,
  created: [] as Record<string, unknown>[],
};

beforeEach(() => {
  gqlMock.mockReset();
  uploadMock.mockReset();
  world.maxBytes = '1048576';
  world.infoFails = false;
  world.createError = null;
  world.created = [];
  gqlMock.mockImplementation((_svc: string, document: unknown, variables: Record<string, unknown>) => {
    const doc = String(document);
    if (doc.includes('query UpdateManagementInfo')) {
      return world.infoFails
        ? Promise.reject(new Error('down'))
        : Promise.resolve({
            updateManagementInfo: { area: 'update-management', allowUnverifiedArtifacts: false, maxArtifactBytes: world.maxBytes },
          });
    }
    if (doc.includes('query DeviceProfiles')) {
      return Promise.resolve({
        deviceProfiles: { results: [{ token: 'edge', name: 'Edge' }], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
      });
    }
    if (doc.includes('mutation CreateArtifact')) {
      const req = variables.request as Record<string, unknown>;
      world.created.push(req);
      if (world.createError) return Promise.reject(world.createError);
      return Promise.resolve({ createArtifact: { ...req, id: 'id', state: 'UPLOADING', trustStatus: 'UNVERIFIED' } });
    }
    throw new Error(`unexpected operation: ${doc.slice(0, 60)}`);
  });
});
afterEach(cleanup);

function file(size: number, name = 'firmware.bin') {
  return new File([new Uint8Array(size)], name, { type: 'application/octet-stream' });
}

async function renderForm(onOpen = vi.fn()) {
  const { container } = render(<NewArtifactForm onOpen={onOpen} />);
  // The size limit must have been read before a file can be judged against it.
  await waitFor(() => expect((container.querySelector('input[type=file]') as HTMLInputElement)).toBeTruthy());
  await waitFor(() => expect(screen.getByRole('button', { name: /Choose file/ }).hasAttribute('disabled')).toBe(false));
  return { container, onOpen };
}

function choose(container: HTMLElement, f: File) {
  fireEvent.change(container.querySelector('input[type=file]')!, { target: { files: [f] } });
}

async function fillAndPick(container: HTMLElement, size = 1024) {
  choose(container, file(size));
  await screen.findByText(/firmware.bin \(/);
  fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'FIRMWARE' } });
  fireEvent.change(screen.getByLabelText('Version'), { target: { value: '1.2.0' } });
  fireEvent.change(screen.getByLabelText('Component'), { target: { value: 'firmware' } });
  fireEvent.click(screen.getByRole('button', { name: 'pick-profile' }));
}
const submit = () => screen.getByRole('button', { name: /Declare and upload|Retry upload/ }) as HTMLButtonElement;

describe('NewArtifactForm refusals before anything is declared', () => {
  it('refuses a file over maxArtifactBytes without declaring or uploading', async () => {
    world.maxBytes = '1000';
    const { container } = await renderForm();

    choose(container, file(2048));

    expect(await screen.findByText(/over the 1000 B this instance accepts/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
    expect(world.created).toHaveLength(0);
    expect(uploadMock).not.toHaveBeenCalled();
  });

  it('accepts a file exactly at the limit', async () => {
    world.maxBytes = '1024';
    const { container } = await renderForm();
    choose(container, file(1024));
    expect(await screen.findByText(/firmware.bin \(/)).toBeTruthy();
    expect(screen.queryByText(/this instance accepts/)).toBeNull();
  });

  it('fails closed when the size limit cannot be read', async () => {
    world.infoFails = true;
    render(<NewArtifactForm onOpen={vi.fn()} />);
    expect(await screen.findByText(/size limit for artifacts could not be read/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });

  it('will not submit without a compatible profile', async () => {
    const { container } = await renderForm();
    choose(container, file(10));
    await screen.findByText(/firmware.bin \(/);
    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'FIRMWARE' } });
    fireEvent.change(screen.getByLabelText('Version'), { target: { value: '1' } });
    fireEvent.change(screen.getByLabelText('Component'), { target: { value: 'firmware' } });
    expect(submit().disabled).toBe(true);
  });
});

describe('NewArtifactForm declare and upload', () => {
  it('declares the file by its real size and digest, then uploads it and shows the verification', async () => {
    uploadMock.mockResolvedValue({ token: 'firmware-1-2-0', state: 'AVAILABLE', sizeBytes: '1024', digest: DIGEST });
    const { container, onOpen } = await renderForm();
    await fillAndPick(container);

    fireEvent.click(submit());

    await waitFor(() => expect(world.created).toHaveLength(1));
    expect(world.created[0]).toMatchObject({
      token: 'firmware-1-2-0',
      kind: 'FIRMWARE',
      name: 'firmware',
      version: '1.2.0',
      component: 'firmware',
      sizeBytes: '1024',
      digest: DIGEST,
      compatibleProfiles: ['edge'],
    });
    expect(uploadMock).toHaveBeenCalledTimes(1);
    expect(uploadMock.mock.calls[0][0]).toBe('firmware-1-2-0');
    expect(await screen.findByText('Content stored and verified')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Open artifact' }));
    expect(onOpen).toHaveBeenCalledWith('firmware-1-2-0');
  });

  it("shows the declaration's refusal verbatim and uploads nothing", async () => {
    world.createError = new Error('ARTIFACT_EXISTS: name, component and version are already declared');
    const { container } = await renderForm();
    await fillAndPick(container);

    fireEvent.click(submit());

    expect(await screen.findByText('ARTIFACT_EXISTS: name, component and version are already declared')).toBeTruthy();
    expect(uploadMock).not.toHaveBeenCalled();
  });

  it("shows the upload's refusal verbatim, keeps the declared artifact, and retries the upload only", async () => {
    uploadMock.mockRejectedValueOnce(
      new Error("the content's SHA-256 does not match the artifact's declared digest"),
    );
    const { container } = await renderForm();
    await fillAndPick(container);

    fireEvent.click(submit());
    expect(await screen.findByText("the content's SHA-256 does not match the artifact's declared digest")).toBeTruthy();
    expect(screen.getByText(/declared but its content is not stored yet/)).toBeTruthy();

    uploadMock.mockResolvedValueOnce({ token: 'firmware-1-2-0', state: 'AVAILABLE', sizeBytes: '1024', digest: DIGEST });
    fireEvent.click(screen.getByRole('button', { name: 'Retry upload' }));

    expect(await screen.findByText('Content stored and verified')).toBeTruthy();
    // A second declaration would collide on the token; the retry must not make one.
    expect(world.created).toHaveLength(1);
    expect(uploadMock).toHaveBeenCalledTimes(2);
  });

  it('treats a success whose echo disagrees with the declaration as a failure', async () => {
    uploadMock.mockResolvedValue({ token: 'firmware-1-2-0', state: 'AVAILABLE', sizeBytes: '1024', digest: 'sha256:' + '0'.repeat(64) });
    const { container, onOpen } = await renderForm();
    await fillAndPick(container);

    fireEvent.click(submit());

    expect(await screen.findByText("The service's answer does not match the declaration")).toBeTruthy();
    expect(screen.queryByText('Content stored and verified')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Open artifact' })).not.toBeNull(); // still reachable to inspect
    expect(onOpen).not.toHaveBeenCalled();
  });
});
