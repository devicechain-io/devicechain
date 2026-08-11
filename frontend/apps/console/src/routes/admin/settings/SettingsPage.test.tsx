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

// ── The defects an adversarial review found, each with the symptom it produced ──
//
// 🔴 Every one of these passed the suite above before it was fixed. They are here
// because "the editors render and the save gate works" was never the hard part.

describe('a draft is form state, not re-derived from JSON', () => {
  // 🔴 The host used to hold serialized JSON and rebuild the form from it on every
  // keystroke, which required each editor's serializer to round-trip losslessly
  // through half-typed states. It did not: Number("33.") is 33, so the decimal
  // point was dropped and fed back into the input as the operator typed it.
  // Latitude and longitude are inherently decimal — this made them unenterable by
  // typing at all, only by pasting a complete value.
  it('lets a decimal coordinate be typed one character at a time', async () => {
    await renderWith([
      setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A"}'),
    ]);
    const lat = document.getElementById('bm-center-lat') as HTMLInputElement;

    fireEvent.change(lat, { target: { value: '33' } });
    fireEvent.change(lat, { target: { value: '33.' } });
    expect(lat.value).toBe('33.');

    fireEvent.change(lat, { target: { value: '33.7' } });
    expect(lat.value).toBe('33.7');
  });

  it('keeps a leading zero and a trailing zero while they are being typed', async () => {
    await renderWith([setting(BASEMAP, '{}')]);
    const zoom = document.getElementById('bm-zoom') as HTMLInputElement;
    fireEvent.change(zoom, { target: { value: '1.50' } });
    expect(zoom.value).toBe('1.50');
  });

  // Same root cause, string fields: the serializer trimmed, so a trailing space
  // was stripped from under the cursor and could never be typed.
  it('lets a trailing space be typed into the attribution', async () => {
    await renderWith([setting(BASEMAP, '{}')]);
    const credit = document.getElementById('bm-attribution') as HTMLInputElement;
    fireEvent.change(credit, { target: { value: '© A ' } });
    expect(credit.value).toBe('© A ');
  });

  // The counterweight: the wire value is still trimmed and coerced. Only the
  // DRAFT is preserved verbatim — otherwise this would just move the bug.
  it('still trims and coerces on the way to the server', async () => {
    await renderWith([setting(BASEMAP, '{}')]);
    fireEvent.change(document.getElementById('bm-tile-url') as HTMLInputElement, {
      target: { value: '  https://a.invalid/{z}/{x}/{y}.png  ' },
    });
    fireEvent.change(document.getElementById('bm-attribution') as HTMLInputElement, {
      target: { value: '© A ' },
    });
    fireEvent.change(document.getElementById('bm-zoom') as HTMLInputElement, {
      target: { value: '9' },
    });
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    fireEvent.click(saveButton());

    await waitFor(() => expect(setSettingMock).toHaveBeenCalled());
    const [, value] = setSettingMock.mock.calls[0] as [string, string];
    expect(JSON.parse(value)).toEqual({
      tileUrl: 'https://a.invalid/{z}/{x}/{y}.png',
      attribution: '© A',
      zoom: 9,
    });
  });
});

describe('a value the editor cannot fully model', () => {
  // 🔴 The server decodes these values CASE-INSENSITIVELY, so `tileURL` binds to
  // the TileURL field and is stored, valid, and serving tiles to every
  // non-overriding tenant. The console read `v.tileUrl` exactly, saw nothing, and
  // mounted the typed editor over it — empty fields above real data. Editing the
  // zoom then saved `{"zoom":10}` and wiped the tile source instance-wide, with no
  // error at any layer. Pinned server-side by
  // TestKeyCasingIsAcceptedBecauseTheJsonDecoderIsCaseInsensitive.
  it('does not load a case-variant key into the typed form', async () => {
    await renderWith([
      setting(BASEMAP, '{"tileURL":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A"}'),
    ]);

    expect(screen.getByText(/not in the shape this editor understands/i)).toBeTruthy();
    // The real value is visible and editable, rather than absent from a form that
    // would then overwrite it.
    expect(rawJson().value).toContain('tileURL');
    expect(document.getElementById('bm-tile-url')).toBeNull();
  });

  // The version-skew case: a field a newer server grew that this console predates.
  // Loading it into the typed form would drop it on the next save.
  it('does not load a value carrying a field this build does not know', async () => {
    await renderWith([setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A","bearing":45}')]);

    expect(rawJson().value).toContain('bearing');
    expect(document.getElementById('bm-tile-url')).toBeNull();
  });

  it('applies the same rule to branding', async () => {
    await renderWith([setting(BRANDING, '{"Title":"Acme"}')]);
    expect(rawJson().value).toContain('Title');
  });

  // The counterweight: a value the editor DOES fully model still gets the form.
  // Without this, a guard that rejected everything would pass all three above.
  it('still loads a value it models completely', async () => {
    await renderWith([
      setting(BASEMAP, '{"tileUrl":"https://a.invalid/{z}/{x}/{y}.png","attribution":"© A"}'),
    ]);
    expect(document.getElementById('bm-tile-url')).toBeTruthy();
    expect(screen.queryByText(/not in the shape this editor understands/i)).toBeNull();
  });
});

describe('the raw JSON editor', () => {
  // 🔴 The fallback section was built inside the host's render body, so it was a
  // NEW component type on every render — React remounted the textarea and dropped
  // focus after each keystroke. The one screen whose purpose is repairing a broken
  // value needed a mouse click per character. The existing typing test missed it
  // because it used the unknown-key path, whose section came from a useMemo.
  it('keeps focus and identity across a keystroke', async () => {
    await renderWith([setting(MASKS, '{"device":42}')]);

    const before = rawJson();
    before.focus();
    expect(document.activeElement).toBe(before);

    fireEvent.change(before, { target: { value: '{"device":"{slug}"}' } });

    const after = rawJson();
    expect(after).toBe(before);
    expect(document.activeElement).toBe(after);
  });

  // It must also not flip out from under the operator the moment their JSON
  // becomes loadable — the editor is chosen once, when the draft is seeded.
  it('does not switch to the typed editor mid-edit', async () => {
    await renderWith([setting(MASKS, '{"device":42}')]);
    fireEvent.change(rawJson(), { target: { value: '{"device":"{slug}"}' } });

    expect(rawJson()).toBeTruthy();
  });
});

describe('what is validated is what would be sent', () => {
  // 🔴 The editors used to validate the DRAFT while toJson independently decided
  // what to emit — two descriptions of "acceptable", agreeing only because the
  // same predicate was written twice. When they disagree the failure is silent:
  // validate passes a draft, toJson quietly omits the field it could not make
  // sense of, and the save succeeds having discarded it.
  //
  // Validating the produced JSON removes the second description. The obligation
  // that replaces it is that toJson be TOTAL — everything typed must appear —
  // which is what this asserts.
  it('carries a half-typed number into the JSON so it can be refused there', async () => {
    await renderWith([setting(BASEMAP, '{}')]);
    fireEvent.change(document.getElementById('bm-zoom') as HTMLInputElement, {
      target: { value: '9x' },
    });

    // Refused — and refused because the value REACHED the JSON, not because a
    // separate check on the form happened to agree with what was emitted.
    expect(saveButton().disabled).toBe(true);
    expect(screen.getByText(/not a number|no es un número/i)).toBeTruthy();
  });

  // 🔴 No branding-height case here, deliberately, and the reason is worth
  // recording: that state is unreachable from BOTH directions. The field is
  // <input type="number">, which filters non-numeric text in the browser and in
  // jsdom, and the server decodes logoMaxHeight into an *int, so a string value
  // is refused before it could ever be stored. The guard in that editor's
  // validate is kept as the counterpart of this one — so the rule lives beside
  // its sibling rather than depending on an input attribute staying put — but a
  // test asserting it would be asserting against a state nothing can produce.
  //
  // The reachable branding equivalent is a non-hex colour, which is a text input
  // and is covered above.
});
