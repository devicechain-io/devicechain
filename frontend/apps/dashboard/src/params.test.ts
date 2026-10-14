// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { parseDashParams } from './params';

describe('parseDashParams', () => {
  it('reads a complete wall-display link', () => {
    const warn = vi.fn();
    expect(
      parseDashParams('?dashboard=site-pulse&kiosk=1&theme=dark&density=wall', warn),
    ).toEqual({ dashboard: 'site-pulse', kiosk: true, theme: 'dark', density: 'wall' });
    expect(warn).not.toHaveBeenCalled();
  });

  it('leaves everything unset, silently, when there are no parameters', () => {
    const warn = vi.fn();
    expect(parseDashParams('', warn)).toEqual({ dashboard: null, kiosk: false, theme: null, density: null });
    expect(warn).not.toHaveBeenCalled();
  });

  it('accepts kiosk=true and an explicit kiosk=0 without warning', () => {
    const warn = vi.fn();
    expect(parseDashParams('?kiosk=true', warn).kiosk).toBe(true);
    expect(parseDashParams('?kiosk=0', warn).kiosk).toBe(false);
    expect(warn).not.toHaveBeenCalled();
  });

  it('ignores each invalid value with a warning and keeps the valid ones', () => {
    const warn = vi.fn();
    const got = parseDashParams('?dashboard=has%20space&kiosk=maybe&theme=neon&density=huge', warn);
    expect(got).toEqual({ dashboard: null, kiosk: false, theme: null, density: null });
    expect(warn).toHaveBeenCalledTimes(4);
    expect(warn.mock.calls.map((c) => String(c[0])).join('\n')).toMatch(/theme.*neon/);

    const mixed = parseDashParams('?theme=neon&density=compact&kiosk=1', vi.fn());
    expect(mixed).toEqual({ dashboard: null, kiosk: true, theme: null, density: 'compact' });
  });

  it('warns on the console by default instead of throwing', () => {
    const spy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    expect(() => parseDashParams('?theme=%E0%A4%A')).not.toThrow();
    spy.mockClear();
    parseDashParams('?density=huge');
    expect(spy).toHaveBeenCalledTimes(1);
    spy.mockRestore();
  });
});
