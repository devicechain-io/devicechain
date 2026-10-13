// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs. The GraphQL transport, the toast and the
// browser's object-URL pair are faked; the component and its API module run for real.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ThingModelDownload, thingModelFileName } from './ThingModelDownload';

const MODEL = '{"@context":"https://www.w3.org/2022/wot/td/v1.1","@type":"tm:ThingModel","title":"Pump"}';

let saved: Blob | null = null;
let clicked: string | null = null;

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  saved = null;
  clicked = null;
  URL.createObjectURL = vi.fn((b: Blob) => {
    saved = b;
    return 'blob:model';
  });
  URL.revokeObjectURL = vi.fn();
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked = this.download;
  });
});

describe('ThingModelDownload', () => {
  it('fetches only the asked-for version and saves it as a Thing Model file', async () => {
    gqlMock.mockResolvedValue({ deviceProfileVersions: [{ version: 2, thingModel: MODEL }] });
    render(<ThingModelDownload profileToken="pump" version={2} latest={5} />);

    fireEvent.click(screen.getByRole('button', { name: 'Download version 2 as a W3C Web of Things Thing Model' }));

    await waitFor(() => expect(clicked).toBe('pump.v2.tm.jsonld'));
    // Newest first: version 2 of 5 sits at offset 3, and one row is asked for.
    expect(gqlMock).toHaveBeenCalledWith('device-management', expect.anything(), { token: 'pump', offset: 3 });
    expect(saved).not.toBeNull();
    expect(saved!.type).toBe('application/td+json');
    expect(await saved!.text()).toBe(MODEL);
    expect(toastMock).not.toHaveBeenCalled();
  });

  it('saves nothing when the list moved and another version came back', async () => {
    gqlMock.mockResolvedValue({ deviceProfileVersions: [{ version: 3, thingModel: MODEL }] });
    render(<ThingModelDownload profileToken="pump" version={2} latest={5} />);

    fireEvent.click(screen.getByRole('button', { name: /Download version 2/ }));

    await waitFor(() =>
      expect(toastMock).toHaveBeenCalledWith('Version 2 could not be exported as a Thing Model.', 'error'),
    );
    expect(clicked).toBeNull();
  });

  it('names the file after the profile and version', () => {
    expect(thingModelFileName('pump', 7)).toBe('pump.v7.tm.jsonld');
  });
});
