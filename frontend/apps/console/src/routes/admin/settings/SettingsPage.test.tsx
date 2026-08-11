// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The system-settings screen and its per-key editors.
//
// What is faked is the SEAM — the settings API and the toast — and nothing else.
// Every editor, its codec and its validation run for real, because those are the
// thing under test: the page's whole reason to exist is that a textarea could not
// tell an operator they were about to store something broken.
//
// 🔴 The assertions are about the properties the old page LACKED — a blocked save
// on an invalid value, a fallback that keeps an unreadable or unknown value
// visible, a draft that survives a glance at another tab. A test that only
// asserted "the fields render" would have passed against the textarea too.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { listSettingsMock, setSettingMock, clearSettingMock, toastMock, confirmMock } = vi.hoisted(
  () => ({
    listSettingsMock: vi.fn(),
    setSettingMock: vi.fn(),
    clearSettingMock: vi.fn(),
    toastMock: vi.fn(),
    confirmMock: vi.fn(),
  }),
);

vi.mock('@/lib/api/settings', () => ({
  listSettings: (...a: unknown[]) => listSettingsMock(...a),
  setSetting: (...a: unknown[]) => setSettingMock(...a),
  clearSetting: (...a: unknown[]) => clearSettingMock(...a),
}));

vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => confirmMock }));

import SettingsPage from './SettingsPage';

interface Row {
  key: string;
  value: string;
  description?: string;
  overridden?: boolean;
  updatedBy?: string | null;
  updatedAt?: string | null;
}

function setting(key: string, value: string, extra: Partial<Row> = {}): Row {
  return { key, value, description: `about ${key}`, overridden: false, ...extra };
}

const MASKS = 'entity.token_masks';
const BASEMAP = 'basemap.default';
const BRANDING = 'branding.default';

async function renderWith(rows: Row[]) {
  listSettingsMock.mockResolvedValue(rows);
  render(<SettingsPage />);
  await screen.findByRole('tablist');
}

/** The Save button for the visible panel. */
function saveButton() {
  return screen.getByRole('button', { name: /save override/i }) as HTMLButtonElement;
}

/** The Reset button for the visible panel. */
function resetButton() {
  return screen.getByRole('button', { name: /reset to default/i }) as HTMLButtonElement;
}

/** Radix Tabs switches on mousedown, so a bare fireEvent.click does nothing. */
function selectTab(name: string | RegExp) {
  fireEvent.mouseDown(screen.getByRole('tab', { name }));
}

function rawJson() {
  return screen.getByRole('textbox', { name: /raw json/i }) as HTMLTextAreaElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  setSettingMock.mockResolvedValue(undefined);
  clearSettingMock.mockResolvedValue(undefined);
  confirmMock.mockResolvedValue(true);
});

afterEach(cleanup);

describe('token masks editor', () => {
  it('renders a row per stored mask, with the sample each one would mint', async () => {
    await renderWith([setting(MASKS, '{"default":"{slug}","device":"device-{numeric-3}"}')]);

    // The sample is the thing the JSON could not show. "{numeric-3}" is three
    // digits, so the operator sees the shape without saving and going to look.
    expect(screen.getByDisplayValue('device-{numeric-3}')).toBeTruthy();
    expect(screen.getByText('device-000')).toBeTruthy();
  });

  it('blocks the save on a mask that would silently mint nothing, and says which', async () => {
    await renderWith([setting(MASKS, '{"device":"device-{alphanumeric-4}"}')]);

    const mask = screen.getByDisplayValue('device-{alphanumeric-4}');
    fireEvent.change(mask, { target: { value: 'dev-{sulg}' } });

    // The defect: an unknown placeholder contributes NOTHING, so this mints "dev-".
    expect(await screen.findByText(/\{sulg\}/)).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
    expect(setSettingMock).not.toHaveBeenCalled();
  });

  it('blocks a mask with no placeholder, which would collide on every entity', async () => {
    await renderWith([setting(MASKS, '{"device":"device-{alphanumeric-4}"}')]);

    fireEvent.change(screen.getByDisplayValue('device-{alphanumeric-4}'), {
      target: { value: 'device' },
    });

    expect(await screen.findByText(/identical token/i)).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  // The counterweight: without it, an editor that blocked EVERYTHING would pass
  // every test above.
  it('saves a valid edit, compacted, under the same key', async () => {
    await renderWith([setting(MASKS, '{"device":"device-{alphanumeric-4}"}')]);

    fireEvent.change(screen.getByDisplayValue('device-{alphanumeric-4}'), {
      target: { value: 'dev-{slug}' },
    });
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    fireEvent.click(saveButton());

    await waitFor(() =>
      expect(setSettingMock).toHaveBeenCalledWith(MASKS, '{"device":"dev-{slug}"}'),
    );
  });

  it('keeps a mask stored for an entity type this build does not know', async () => {
    await renderWith([setting(MASKS, '{"some-future-type":"{slug}"}')]);

    // Visible, editable, and flagged — not dropped, which is what a picker over a
    // closed vocabulary would otherwise do to it. The picker's accessible name is
    // its label AND its current value, which is why both appear here.
    expect(screen.getByText(/unrecognized entity type/i)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Entity type some-future-type' })).toBeTruthy();
  });
});

describe('the raw JSON fallback', () => {
  it('shows a key the server knows and this console has no editor for', async () => {
    await renderWith([setting('some.future.key', '{"a":1}')]);

    // The tab is labelled with the key itself, and the value is editable.
    expect(screen.getByRole('tab', { name: 'some.future.key' })).toBeTruthy();
    expect(rawJson()).toBeTruthy();
  });

  it('falls back when a stored value is beyond its own editor, and says so', async () => {
    // A token-masks value whose entries are not strings: legal JSON, wrong shape.
    await renderWith([setting(MASKS, '{"device":42}')]);

    expect(screen.getByText(/not in the shape this editor understands/i)).toBeTruthy();
    expect(rawJson().value).toBe(JSON.stringify({ device: 42 }, null, 2));
  });

  it('blocks the save on raw JSON that does not parse', async () => {
    await renderWith([setting('some.future.key', '{"a":1}')]);

    fireEvent.change(rawJson(), { target: { value: '{ not json' } });

    expect(await screen.findByText(/must be valid json/i)).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });
});

describe('the shared frame', () => {
  it('keeps a draft when another tab is visited', async () => {
    // 🔴 Radix unmounts the inactive panel, so a draft held inside a panel would
    // be discarded by a glance at another tab. This is why drafts live in the page.
    await renderWith([
      setting(MASKS, '{"device":"device-{alphanumeric-4}"}'),
      setting('some.future.key', '{"a":1}'),
    ]);

    fireEvent.change(screen.getByDisplayValue('device-{alphanumeric-4}'), {
      target: { value: 'dev-{slug}' },
    });
    // Radix Tabs activates on mousedown (and on focus), not on a bare click event.
    selectTab('some.future.key');
    await waitFor(() => expect(rawJson()).toBeTruthy());
    selectTab(/token masks/i);

    expect(await screen.findByDisplayValue('dev-{slug}')).toBeTruthy();
  });

  it('offers Reset only for an overridden setting', async () => {
    await renderWith([setting(MASKS, '{"default":"{slug}"}', { overridden: false })]);
    expect(resetButton().disabled).toBe(true);

    cleanup();
    await renderWith([
      setting(MASKS, '{"default":"{slug}"}', { overridden: true, updatedBy: 'op@example.invalid' }),
    ]);
    expect(resetButton().disabled).toBe(false);
    expect(screen.getByText(/op@example.invalid/)).toBeTruthy();

    fireEvent.click(resetButton());
    await waitFor(() => expect(clearSettingMock).toHaveBeenCalledWith(MASKS));
  });

  it('does not treat re-formatting as an edit', async () => {
    // The stored value arrives compact and is shown pretty-printed; that is
    // formatting, not a change, and Save must stay disabled until something real
    // happens. Otherwise every tab opens dirty and Save means nothing.
    await renderWith([setting(MASKS, '{"default":"{slug}"}')]);
    expect(saveButton().disabled).toBe(true);
  });
});

describe('instance defaults reuse the tenant editors', () => {
  it('edits basemap.default through the same fields the tenant editor uses', async () => {
    await renderWith([setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A"}')]);

    // The provider picker and the raw fields are the tenant editor's, mounted here.
    expect(screen.getByDisplayValue('https://a.invalid/{z}/{x}/{y}.png')).toBeTruthy();
    expect(screen.getByDisplayValue('© A')).toBeTruthy();
  });

  it('blocks a tile URL with no credit line at the instance tier too', async () => {
    await renderWith([setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A"}')]);

    fireEvent.change(screen.getByDisplayValue('© A'), { target: { value: '' } });

    expect(saveButton().disabled).toBe(true);
  });

  // 🔴 #704's field-loss shape: Number('12x') is NaN and JSON.stringify writes NaN
  // as null, so a stray character in a camera field must BLOCK rather than reach
  // the wire as a cleared value.
  it('never writes a non-numeric camera field to the wire as null', async () => {
    await renderWith([
      setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A","zoom":9}'),
    ]);

    fireEvent.change(screen.getByDisplayValue('9'), { target: { value: '9x' } });

    expect(saveButton().disabled).toBe(true);
    expect(setSettingMock).not.toHaveBeenCalled();
  });

  it('omits an empty branding field rather than storing null', async () => {
    await renderWith([setting(BRANDING, '{"title":"Acme","primary":"#1f9fb7"}')]);

    fireEvent.change(screen.getByDisplayValue('Acme'), { target: { value: '' } });
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    fireEvent.click(saveButton());

    await waitFor(() => expect(setSettingMock).toHaveBeenCalled());
    const [, value] = setSettingMock.mock.calls[0] as [string, string];
    expect(JSON.parse(value)).toEqual({ primary: '#1f9fb7' });
  });

  it('blocks a non-hex branding colour', async () => {
    await renderWith([setting(BRANDING, '{"primary":"#1f9fb7"}')]);

    fireEvent.change(screen.getByDisplayValue('#1f9fb7'), { target: { value: 'blue' } });

    expect(saveButton().disabled).toBe(true);
  });
});

describe('the tab bar', () => {
  it('names each known setting and falls back to the key for the rest', async () => {
    await renderWith([
      setting(MASKS, '{"default":"{slug}"}'),
      setting(BRANDING, '{}'),
      setting(BASEMAP, '{}'),
      setting('some.future.key', '{}'),
    ]);

    const tabs = within(screen.getByRole('tablist')).getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual([
      'Token masks',
      'Branding',
      'Map',
      'some.future.key',
    ]);
  });
});
