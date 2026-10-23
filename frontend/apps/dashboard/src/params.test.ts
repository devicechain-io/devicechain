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

describe('rotate and every (the kiosk playlist link)', () => {
  it('reads a playlist, implies kiosk, and defaults the interval to a minute', () => {
    const warn = vi.fn();
    const got = parseDashParams('?rotate=wall-a,wall-b,wall-c', warn);
    expect(got.playlist).toEqual({ tokens: ['wall-a', 'wall-b', 'wall-c'], everySeconds: 60 });
    expect(got.kiosk).toBe(true);
    expect(got.rotateIssues).toBeUndefined();
    expect(warn).not.toHaveBeenCalled();
  });

  it('reads every, in whole seconds from the minimum to the maximum', () => {
    const warn = vi.fn();
    for (const [raw, seconds] of [['10', 10], ['45', 45], ['3600', 3600]] as const) {
      expect(parseDashParams(`?rotate=a,b&every=${raw}`, warn).playlist?.everySeconds, raw).toBe(seconds);
    }
    expect(warn).not.toHaveBeenCalled();
  });

  it.each([
    ['9', 'tooShort'],
    ['0', 'tooShort'],
    ['3601', 'tooLong'],
    ['999999', 'tooLong'],
    ['1.5', 'unparseable'],
    ['-5', 'unparseable'],
    ['abc', 'unparseable'],
    ['', 'unparseable'],
    ['1e3', 'unparseable'],
  ])('ignores every=%s (%s) with a note and plays at the default', (raw, reason) => {
    const warn = vi.fn();
    const got = parseDashParams(`?rotate=a,b&every=${encodeURIComponent(raw)}`, warn);
    expect(got.playlist?.everySeconds).toBe(60);
    expect(got.rotateIssues).toEqual([{ kind: 'every', value: raw, reason }]);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('skips a token that is not one, notes it, and plays the rest', () => {
    const warn = vi.fn();
    const got = parseDashParams('?rotate=a,not%20a%20token,b,,%3Cscript%3E', warn);
    expect(got.playlist?.tokens).toEqual(['a', 'b']);
    expect(got.rotateIssues).toEqual([
      { kind: 'token', value: 'not a token' },
      { kind: 'token', value: '<script>' },
    ]);
  });

  it('keeps a board listed twice, in order', () => {
    expect(parseDashParams('?rotate=a,b,a').playlist?.tokens).toEqual(['a', 'b', 'a']);
  });

  it('caps the playlist and says how many were dropped', () => {
    const tokens = Array.from({ length: 30 }, (_, i) => `board-${i}`);
    const got = parseDashParams(`?rotate=${tokens.join(',')}`, vi.fn());
    expect(got.playlist?.tokens).toHaveLength(24);
    expect(got.rotateIssues).toEqual([{ kind: 'too-many', dropped: 6 }]);
  });

  it('is no playlist below two boards: one opens that board, none opens the list, both say so', () => {
    const one = parseDashParams('?rotate=only-one', vi.fn());
    expect(one.playlist).toBeUndefined();
    expect(one.dashboard).toBe('only-one');
    expect(one.kiosk).toBe(false);
    expect(one.rotateIssues).toEqual([{ kind: 'too-few', usable: 1 }]);
    const none = parseDashParams('?rotate=,,', vi.fn());
    expect(none.playlist).toBeUndefined();
    expect(none.dashboard).toBeNull();
    expect(none.rotateIssues).toEqual([{ kind: 'too-few', usable: 0 }]);
    const bad = parseDashParams('?rotate=%20bad%20token,good', vi.fn());
    expect(bad.dashboard).toBe('good');
    expect(bad.rotateIssues).toEqual([{ kind: 'token', value: 'bad token' }, { kind: 'too-few', usable: 1 }]);
  });

  it('lets the playlist win over dashboard, and says so', () => {
    const got = parseDashParams('?dashboard=x&rotate=a,b', vi.fn());
    expect(got.dashboard).toBeNull();
    expect(got.rotateIssues).toEqual([{ kind: 'dashboard-ignored' }]);
  });

  it('notes every without rotate, and keeps the rest of the link', () => {
    const warn = vi.fn();
    const got = parseDashParams('?dashboard=x&every=30&range=6h', warn);
    expect(got).toMatchObject({ dashboard: 'x', range: 21600 });
    expect(got.playlist).toBeUndefined();
    expect(got.rotateIssues).toEqual([{ kind: 'every-alone' }]);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('keeps the time range, fixed window and theme alongside the playlist', () => {
    const got = parseDashParams('?rotate=a,b&every=20&theme=dark&range=24h&from=2026-10-14T02:00:00Z&to=2026-10-14T04:00:00Z', vi.fn());
    expect(got).toMatchObject({ theme: 'dark', range: 86400, playlist: { everySeconds: 20 } });
    expect(got.absolute).toBeDefined();
  });
});
