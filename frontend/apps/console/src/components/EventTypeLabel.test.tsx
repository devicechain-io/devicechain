// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom (set globally in vite.config.ts). The real catalogs are wired
// in by importing the i18n config for its side effect, so these assertions are on
// the STRINGS AN OPERATOR SEES — a key added to the map but not to `en` would
// render its own key name here and fail, which a mocked translator would hide.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { EventTypeLabel } from './EventTypeLabel';

afterEach(cleanup);

// The ordinals as the backend defines them (event-sources model), paired with the
// English label each must produce. Written out longhand rather than derived from
// the component's own table: a test that reads the map under test would agree
// with any renumbering of it.
const KNOWN: [number, string][] = [
  [0, 'Relationship'],
  [1, 'Location'],
  [2, 'Measurement'],
  [3, 'Alert'],
  [4, 'State change'],
  [5, 'Command invocation'],
  [6, 'Command response'],
];

describe('EventTypeLabel', () => {
  it.each(KNOWN)('renders ordinal %i as its name', (ordinal, label) => {
    render(<EventTypeLabel eventType={ordinal} />);
    expect(screen.getByText(label)).toBeTruthy();
    // And never as the raw ordinal it arrived as — the defect this replaces.
    expect(screen.queryByText(`#${ordinal}`)).toBeNull();
  });

  // 🔴 The one that matters most about this table. An eighth event type will exist
  // before this console knows about it, and the failure mode to prevent is not a
  // blank — it is a CONFIDENT WRONG NAME. Falling back to the last known label
  // would render ordinal 7 as "Command response", indistinguishable from a real
  // one; naming the number instead is the only honest answer.
  it('renders an unknown ordinal as unknown, naming the number', () => {
    render(<EventTypeLabel eventType={7} />);
    expect(screen.getByText('Unknown type (#7)')).toBeTruthy();
    for (const [, label] of KNOWN) {
      expect(screen.queryByText(label), `ordinal 7 must not display as "${label}"`).toBeNull();
    }
  });

  it('treats a negative ordinal as unknown too', () => {
    render(<EventTypeLabel eventType={-1} />);
    expect(screen.getByText('Unknown type (#-1)')).toBeTruthy();
  });
});
