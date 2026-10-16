// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { parseDashParams } from './params';

describe('parseDashParams', () => {
  it('reads a complete wall-display link', () => {
    const warn = vi.fn();
    expect(
      parseDashParams('?dashboard=site-pulse&kiosk=1&theme=dark&density=wall', warn),
    ).toEqual({ dashboard: 'site-pulse', kiosk: true, kioskChrome: true, theme: 'dark', density: 'wall', range: null, paused: false });
    expect(warn).not.toHaveBeenCalled();
  });

  it('leaves everything unset, silently, when there are no parameters', () => {
    const warn = vi.fn();
    expect(parseDashParams('', warn)).toEqual({ dashboard: null, kiosk: false, kioskChrome: true, theme: null, density: null, range: null, paused: false });
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
    expect(got).toEqual({ dashboard: null, kiosk: false, kioskChrome: true, theme: null, density: null, range: null, paused: false });
    expect(warn).toHaveBeenCalledTimes(4);
    expect(warn.mock.calls.map((c) => String(c[0])).join('\n')).toMatch(/theme.*neon/);

    const mixed = parseDashParams('?theme=neon&density=compact&kiosk=1', vi.fn());
    expect(mixed).toEqual({ dashboard: null, kiosk: true, kioskChrome: true, theme: null, density: 'compact', range: null, paused: false });
  });

  it('warns on the console by default instead of throwing', () => {
    const spy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    expect(() => parseDashParams('?theme=%E0%A4%A')).not.toThrow();
    spy.mockClear();
    parseDashParams('?density=huge');
    expect(spy).toHaveBeenCalledTimes(1);
    spy.mockRestore();
  });

  describe('range and paused (the time-range deep link)', () => {
    it('reads each preset as its span in seconds', () => {
      const warn = vi.fn();
      const spans = ['15m', '1h', '6h', '24h', '7d'].map((r) => parseDashParams(`?range=${r}`, warn).range);
      expect(spans).toEqual([900, 3600, 21600, 86400, 604800]);
      expect(warn).not.toHaveBeenCalled();
    });

    it('opens frozen only for paused=1, and reads a complete link', () => {
      const warn = vi.fn();
      expect(parseDashParams('?range=1h&paused=1', warn)).toMatchObject({ range: 3600, paused: true });
      expect(parseDashParams('?paused=0', warn).paused).toBe(false);
      expect(warn).not.toHaveBeenCalled();
    });

    it('ignores every unknown range with a warning, and uses the default (null), never throwing', () => {
      const warn = vi.fn();
      // A span in the wrong unit, wrong case, a number, an empty value, out of the preset set, and
      // something that would be dangerous if it were honoured.
      for (const bad of ['2h', '1H', '3600', '', '30d', '-1h', '1h ', '%00', '9999999999d', 'NaN']) {
        warn.mockClear();
        const got = parseDashParams(`?range=${encodeURIComponent(bad)}`, warn);
        expect(got.range, JSON.stringify(bad)).toBeNull();
        expect(warn, JSON.stringify(bad)).toHaveBeenCalledTimes(1);
      }
    });

    it('opens LIVE for a malformed paused, warning once per bad value', () => {
      const warn = vi.fn();
      for (const bad of ['true', 'yes', '2', '', '1 ', 'on']) {
        warn.mockClear();
        expect(parseDashParams(`?paused=${encodeURIComponent(bad)}`, warn).paused, JSON.stringify(bad)).toBe(false);
        expect(warn, JSON.stringify(bad)).toHaveBeenCalledTimes(1);
      }
    });

    it('keeps the valid half when the other is invalid', () => {
      const warn = vi.fn();
      expect(parseDashParams('?range=bogus&paused=1', warn)).toMatchObject({ range: null, paused: true });
      expect(parseDashParams('?range=6h&paused=maybe', warn)).toMatchObject({ range: 21600, paused: false });
    });
  });
});
