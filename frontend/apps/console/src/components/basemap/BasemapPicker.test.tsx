// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provider picker's behaviour. Nothing is faked here but the onChange sink — the
// catalog data is the real catalog, and i18n is the real i18n, because the two things
// most worth proving are that a real entry composes a storable value and that the
// escape hatch does not destroy what someone typed.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { selectOption, selectedLabel } from '@/test/select';

import { BasemapPicker, type TileSource } from './BasemapPicker';
import { API_KEY_TOKEN, PROVIDERS, composeTileUrl, needsApiKey } from './catalog';

afterEach(cleanup);

const OSM = PROVIDERS.find((p) => p.id === 'openstreetmap')!;
const CARTO = PROVIDERS.find((p) => p.id === 'carto')!;
const KEYED = PROVIDERS.find((p) => p.sources.some(needsApiKey))!;

function mount(source: Partial<TileSource> = {}) {
  const onChange = vi.fn<(next: TileSource) => void>();
  const value: TileSource = {
    tileUrl: source.tileUrl ?? '',
    attribution: source.attribution ?? '',
  };
  const view = render(
    <BasemapPicker tileUrl={value.tileUrl} attribution={value.attribution} onChange={onChange} />,
  );
  const rerender = (next: TileSource) =>
    view.rerender(
      <BasemapPicker tileUrl={next.tileUrl} attribution={next.attribution} onChange={onChange} />,
    );
  return { onChange, rerender };
}

/** The most recent emission. Index arithmetic rather than `.at`, which the console's
 *  ES2020 lib target does not carry. */
function lastEmit(onChange: { mock: { calls: [TileSource][] } }): TileSource {
  const { calls } = onChange.mock;
  expect(calls.length, 'expected the picker to have emitted').toBeGreaterThan(0);
  return calls[calls.length - 1][0];
}

// 🔴 These are TRIGGERS, not <select> elements — the kit's Select is a Radix listbox
// so it can be themed (a native option list is drawn by the OS and ignores our
// theme entirely). They are still found by label, but they are read with
// selectedLabel() and driven with selectOption() rather than `.value` and
// fireEvent.change, both of which silently do nothing on a button.
const providerSelect = () => screen.getByLabelText('Provider');
const styleSelect = () => screen.getByLabelText('Style');
const keyInput = () => screen.getByLabelText('API key') as HTMLInputElement;

describe('choosing a catalog provider', () => {
  it('fills the tile URL and the credit line together, in ONE change', async () => {
    const { onChange } = mount();
    await selectOption(providerSelect(), OSM.name);

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({
      tileUrl: OSM.sources[0].tileUrl,
      attribution: OSM.sources[0].attribution,
    });
  });

  // 🔴 The property, stated as a property rather than as one expected pair: a picker
  // that emitted a URL with a stale or empty attribution would be manufacturing a
  // licence violation, so no emission may ever carry one without the other.
  it('never emits a tile URL without the attribution that belongs to it', async () => {
    const { onChange, rerender } = mount();
    for (const provider of PROVIDERS) {
      await selectOption(providerSelect(), provider.name);
      const emitted = lastEmit(onChange);
      const matching = provider.sources.find(
        (s) => composeTileUrl(s.tileUrl, '') === emitted.tileUrl,
      );
      expect(matching, `emitted URL should be one of ${provider.id}'s`).toBeTruthy();
      expect(emitted.attribution).toBe(matching!.attribution);
      rerender(emitted);
    }
  });

  it('reads a stored URL back as its provider instead of showing Custom', async () => {
    mount({ tileUrl: CARTO.sources[2].tileUrl, attribution: CARTO.sources[2].attribution });
    expect(selectedLabel(providerSelect())).toBe(CARTO.name);
    expect(selectedLabel(styleSelect())).toBe(CARTO.sources[2].name);
  });

  it('offers a style list only for a provider that has more than one', async () => {
    const { rerender } = mount({
      tileUrl: OSM.sources[0].tileUrl,
      attribution: OSM.sources[0].attribution,
    });
    expect(OSM.sources).toHaveLength(1);
    expect(screen.queryByLabelText('Style')).toBeNull();

    rerender({ tileUrl: CARTO.sources[0].tileUrl, attribution: CARTO.sources[0].attribution });
    expect(screen.getByLabelText('Style')).toBeTruthy();
  });
});

describe('the API key is a field, not something pasted into a URL', () => {
  it('holds the placeholder until a key is supplied, so the value cannot be saved half-made', async () => {
    const { onChange, rerender } = mount();
    await selectOption(providerSelect(), KEYED.name);

    const emitted = lastEmit(onChange);
    expect(emitted.tileUrl).toContain(API_KEY_TOKEN);
    // The picker is fully controlled — it derives everything from its props — so the
    // emitted value has to come back round before the warning it causes can render.
    // That round trip is the component's contract, not a testing detail.
    rerender(emitted);
    expect(screen.getByTestId('basemap-key-missing')).toBeTruthy();
  });

  it('composes a typed key into the template', async () => {
    const keyedSource = KEYED.sources.find(needsApiKey)!;
    const { onChange, rerender } = mount({
      tileUrl: keyedSource.tileUrl,
      attribution: keyedSource.attribution,
    });

    fireEvent.change(keyInput(), { target: { value: 'secret-key' } });
    const emitted = lastEmit(onChange);
    expect(emitted.tileUrl).not.toContain(API_KEY_TOKEN);
    expect(emitted.tileUrl).toContain('secret-key');

    rerender(emitted);
    expect(screen.queryByTestId('basemap-key-missing')).toBeNull();
    expect(keyInput().value).toBe('secret-key');
  });

  it('keeps the key when the style changes, rather than making it be retyped', async () => {
    const keyed = KEYED.sources.filter(needsApiKey);
    expect(keyed.length, 'need a multi-style keyed provider for this').toBeGreaterThan(1);

    const withKey = composeTileUrl(keyed[0].tileUrl, 'secret-key');
    const { onChange } = mount({ tileUrl: withKey, attribution: keyed[0].attribution });

    await selectOption(styleSelect(), keyed[1].name);
    const emitted = lastEmit(onChange);
    expect(emitted.tileUrl).toBe(composeTileUrl(keyed[1].tileUrl, 'secret-key'));
    expect(emitted.tileUrl).toContain('secret-key');
  });

  it('shows no key field for a provider that needs none', async () => {
    mount({ tileUrl: OSM.sources[0].tileUrl, attribution: OSM.sources[0].attribution });
    expect(screen.queryByLabelText('API key')).toBeNull();
  });
});

// 🔴 THE NEGATIVE CONTROL THE SPEC ASKED FOR, and the one that would hurt most if it
// were missing: "Custom…" is what someone picks when the catalog does not have their
// provider, which is exactly the moment they have already typed something.
describe('Custom… is an escape hatch, not an eraser', () => {
  it('leaves the fields completely untouched', async () => {
    const mine: TileSource = {
      tileUrl: 'https://tiles.internal.example.com/{z}/{x}/{y}.png',
      attribution: '© Example Corp',
    };
    const { onChange } = mount(mine);

    expect(selectedLabel(providerSelect())).toBe('Custom…');
    await selectOption(providerSelect(), 'Custom…');
    expect(onChange).not.toHaveBeenCalled();
  });

  it('does not overwrite a catalog value either, when re-selected from one', async () => {
    const { onChange } = mount({
      tileUrl: OSM.sources[0].tileUrl,
      attribution: OSM.sources[0].attribution,
    });
    expect(selectedLabel(providerSelect())).toBe(OSM.name);

    await selectOption(providerSelect(), 'Custom…');
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe('an edited credit line is called out', () => {
  it('warns when the attribution no longer matches the chosen provider', async () => {
    mount({ tileUrl: OSM.sources[0].tileUrl, attribution: '© Acme' });
    expect(screen.getByTestId('basemap-attribution-drift')).toBeTruthy();
  });

  // The control: the warning must be able to be absent, or it says nothing.
  it('stays quiet when the attribution is the one the catalog supplied', async () => {
    mount({ tileUrl: OSM.sources[0].tileUrl, attribution: OSM.sources[0].attribution });
    expect(screen.queryByTestId('basemap-attribution-drift')).toBeNull();
  });

  it('says nothing at all about a custom tile source, whose credit line we cannot know', async () => {
    mount({ tileUrl: 'https://tiles.internal.example.com/{z}/{x}/{y}.png', attribution: '© Me' });
    expect(screen.queryByTestId('basemap-attribution-drift')).toBeNull();
  });
});

describe('provider links', () => {
  it('links the provider\'s own terms rather than paraphrasing them', async () => {
    mount({ tileUrl: OSM.sources[0].tileUrl, attribution: OSM.sources[0].attribution });
    const link = screen.getByRole('link', { name: /terms and pricing/i }) as HTMLAnchorElement;
    expect(link.href).toBe(OSM.termsUrl);
  });

  it('sends someone to the key page for a provider that needs a key', async () => {
    const keyedSource = KEYED.sources.find(needsApiKey)!;
    mount({ tileUrl: keyedSource.tileUrl, attribution: keyedSource.attribution });
    const link = screen.getByRole('link', { name: /where do i get a key/i }) as HTMLAnchorElement;
    expect(link.href).toBe(KEYED.keyUrl);
  });
});
