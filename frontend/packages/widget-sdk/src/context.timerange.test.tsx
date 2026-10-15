// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board's time range as a widget reads it. Additive public API: a third-party widget asks
// useWidgetTimeRange() and gets the span and the pause, or undefined where no board imposes one.

import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { WidgetTimeRangeProvider, useWidgetTimeRange } from './context';
import * as sdk from './index';

function Probe({ onRead }: { onRead: (v: unknown) => void }) {
  onRead(useWidgetTimeRange());
  return null;
}

describe('useWidgetTimeRange', () => {
  it('is undefined outside a board: no range is imposed, which is not a default span', () => {
    let seen: unknown = 'unset';
    render(<Probe onRead={(v) => (seen = v)} />);
    expect(seen).toBeUndefined();
  });

  it('carries the span and the pause a host provides', () => {
    let seen: unknown;
    render(
      <WidgetTimeRangeProvider range={{ spanSeconds: 21600, paused: true, pausedAt: 1234 }}>
        <Probe onRead={(v) => (seen = v)} />
      </WidgetTimeRangeProvider>,
    );
    expect(seen).toEqual({ spanSeconds: 21600, paused: true, pausedAt: 1234 });
  });

  it('is part of the public surface, alongside the contexts it joins', () => {
    expect(typeof sdk.useWidgetTimeRange).toBe('function');
    expect(typeof sdk.WidgetTimeRangeProvider).toBe('function');
    expect(typeof sdk.useWidgetTimeZone).toBe('function');
  });

  it('did not touch the SDK generation: additive, so existing widgets keep resolving', () => {
    expect(sdk.SDK_API_VERSION).toBe(1);
  });
});
