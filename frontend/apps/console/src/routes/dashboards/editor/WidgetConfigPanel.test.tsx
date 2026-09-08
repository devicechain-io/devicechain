// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The map widget's basemap options.
//
// 🔴 A widget's tile source is the one tier of the basemap cascade that never reaches
// the server. The tenant and instance tiers are validated at the mutation and cannot
// store a tile URL without the credit line its licence requires; a widget option is
// typed into this panel, stored in the dashboard definition as opaque JSON, and handed
// straight to the renderer. So the pair is enforced at RENDER — an option naming tiles
// with no attribution is discarded and the tenant's basemap is drawn instead.
//
// That rule is correct and completely invisible, which is its own defect: the author
// sees a map, just not theirs, and concludes the option is broken. This panel is where
// they are standing when it happens, so this is where it has to be said.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { WidgetConfigPanel } from './WidgetConfigPanel';
import type { WidgetInstance, WidgetType } from '@devicechain/dashboards';

afterEach(cleanup);

function widget(options: Record<string, unknown>, type: WidgetType = 'map'): WidgetInstance {
  // A real layout rather than a cast: `as WidgetInstance` on a wrong shape compiles
  // happily and would leave this suite testing a widget the renderer could not place.
  return {
    id: 'w1',
    type,
    layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 4, z: 0 } },
    options,
  };
}

function renderPanel(options: Record<string, unknown>, type: WidgetType = 'map') {
  render(
    <WidgetConfigPanel
      widget={widget(options, type)}
      datasource={undefined}
      onChange={vi.fn()}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />,
  );
}

const warning = () => screen.queryByTestId('widget-tile-uncredited');

describe('a map widget tile source without its credit line', () => {
  it('is called out, because the renderer will silently ignore it', () => {
    renderPanel({ tileUrl: 'https://tiles.example.com/{z}/{x}/{y}.png' });
    expect(warning()).toBeTruthy();
  });

  // 🔴 THE DEFAULT-STATE CONTROL, and it is here because its absence let a mutation
  // survive on the geofence editor's identical warning: dropping the tileUrl half of
  // the condition passed that whole suite, because no test rendered the form in the
  // state nearly every author is actually in — nothing entered at all.
  //
  // A control proving a warning CAN be absent is not the same as proving it is absent
  // on the input that will OCCUR.
  it('says nothing when no tile source has been entered at all', () => {
    renderPanel({});
    expect(warning()).toBeNull();
  });

  it('says nothing once both halves are present', () => {
    renderPanel({
      tileUrl: 'https://tiles.example.com/{z}/{x}/{y}.png',
      attribution: '© Example',
    });
    expect(warning()).toBeNull();
  });

  // The mirror image: an attribution alone names no tiles, so it is not the warning's
  // subject and must not trigger it.
  it('says nothing for an attribution with no tile source', () => {
    renderPanel({ attribution: '© Example' });
    expect(warning()).toBeNull();
  });

  // Whitespace is not a tile source anywhere else in this feature; it is not one here.
  it('treats a whitespace-only tile URL as absent', () => {
    renderPanel({ tileUrl: '   ' });
    expect(warning()).toBeNull();
  });

  // 🔴 THE OTHER SUBJECT CONTROL. Which widget this warning belongs to is decided from the
  // option schema — "does this type declare both halves of the tile pair?" — rather than
  // from a `type === 'map'` test, so that a second map widget cannot ship without it. That
  // makes the reverse worth pinning: a leftover `tileUrl` on a widget that reads no tiles
  // is a stray key for the publish validator to report, not a licence problem to warn
  // about, and a condition that had quietly become "the options are present" would say so
  // on every widget on the board.
  it('is not raised for a widget that reads no tiles, whatever its options carry', () => {
    renderPanel({ tileUrl: 'https://tiles.example.com/{z}/{x}/{y}.png' }, 'gauge');
    expect(warning()).toBeNull();
  });
});
