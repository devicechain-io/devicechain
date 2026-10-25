// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's URL parameters, for a deep link a wall display or a bookmark can carry:
//
//   /dash/?dashboard=<token>&kiosk=1&theme=dark&density=wall
//
//   dashboard  the dashboard token to load right after sign-in
//   kiosk      1 | true  hide the chrome and offer fullscreen
//   chrome     0 | false  with kiosk: also hide the kiosk's own floating controls (the
//                         connection badge and the fullscreen button), for a capture or an
//                         embed that frames the board itself. A paused board still says so.
//   theme      dark | light
//   density    compact | comfortable | wall
//   range      15m | 1h | 6h | 24h | 7d  the time range the board opens at
//   from, to   an ABSOLUTE window, both required: ISO 8601 WITH a zone (`Z` or `+02:00`), e.g.
//              from=2026-10-22T02:00:00Z&to=2026-10-22T04:00:00Z. Needs from < to, a span of one
//              minute to 31 days, and an end that is not in the future. The live tail is off while
//              it shows, and it wins over range/paused. Unusable values are ignored WITH a note
//              on the page (not only a console warning), and the board opens live.
//   paused     1  open frozen at the moment of loading (any other value opens live)
//   rotate     tokA,tokB,...  a kiosk PLAYLIST: cycle these published boards (2 to 24 tokens; implies kiosk).
//              A token that is not one is skipped and noted; fewer than two usable boards is no playlist.
//   every      seconds each board shows, 10 to 3600 (default 60). Only with rotate.
//              The playlist keeps the link's other parameters on every board: the same time range or
//              fixed window, and each `bind.<slot>` on the boards that have that slot.
//
// A parameter that is absent is simply unset. One that is PRESENT BUT INVALID is ignored
// with a console warning: a typo in a bookmarked wall URL must still bring the board up
// (with the default for that one setting) rather than a blank screen nobody is there to read.

import { isValidToken } from '@devicechain/client';
import {
  TIME_RANGE_PRESETS,
  parseAbsoluteParams,
  parseRangeParam,
  type AbsoluteLinkRejection,
  type AbsoluteRange,
} from '@devicechain/dashboards';

export const ROTATE_MIN_BOARDS = 2;
export const ROTATE_MAX_BOARDS = 24;
export const ROTATE_MIN_SECONDS = 10;
export const ROTATE_MAX_SECONDS = 3600;
export const ROTATE_DEFAULT_SECONDS = 60;

/** Why part of a `rotate`/`every` link was set aside, in a form that carries no language. */
export type RotateIssue =
  | { kind: 'token'; value: string }
  | { kind: 'too-few'; usable: number }
  | { kind: 'too-many'; dropped: number }
  | { kind: 'every'; value: string; reason: 'unparseable' | 'tooShort' | 'tooLong' }
  | { kind: 'every-alone' }
  | { kind: 'dashboard-ignored' };

export interface Playlist {
  tokens: string[];
  everySeconds: number;
}

export type DashTheme = 'dark' | 'light';
export type DashDensity = 'compact' | 'comfortable' | 'wall';

export interface DashParams {
  dashboard: string | null;
  kiosk: boolean;
  // With kiosk: whether the kiosk's floating controls show (true unless chrome=0).
  kioskChrome: boolean;
  theme: DashTheme | null;
  density: DashDensity | null;
  // The time range's span in seconds (one of the presets), or null for the board's own default.
  range: number | null;
  // Open frozen. Only `paused=1` freezes: a malformed value opens LIVE, never frozen on an
  // instant nobody chose.
  paused: boolean;
  // A valid `from`+`to` window; absent otherwise.
  absolute?: AbsoluteRange;
  // `from`/`to` were given but unusable: why, so the page can say the link was ignored.
  rejected?: AbsoluteLinkRejection;
  // A usable `rotate` (two or more boards): the playlist the kiosk cycles. `kiosk` is then true.
  playlist?: Playlist;
  // Everything about `rotate`/`every` that was set aside, so the page can say so.
  rotateIssues?: RotateIssue[];
}

const THEMES: readonly DashTheme[] = ['dark', 'light'];
const DENSITIES: readonly DashDensity[] = ['compact', 'comfortable', 'wall'];
const TRUE_VALUES = ['1', 'true'];
const FALSE_VALUES = ['0', 'false'];

export function parseDashParams(
  search: string,
  warn: (message: string) => void = (m) => console.warn(m),
): DashParams {
  const q = new URLSearchParams(search);
  const bad = (name: string, value: string, expected: string) =>
    warn(`/dash: ignoring ${name}=${JSON.stringify(value)} (expected ${expected})`);

  let dashboard: string | null = null;
  const d = q.get('dashboard');
  if (d !== null) {
    if (isValidToken(d)) dashboard = d;
    else bad('dashboard', d, 'a dashboard token');
  }

  let kiosk = false;
  const k = q.get('kiosk');
  if (k !== null) {
    if (TRUE_VALUES.includes(k)) kiosk = true;
    else if (!FALSE_VALUES.includes(k)) bad('kiosk', k, '1 or 0');
  }

  let kioskChrome = true;
  const c = q.get('chrome');
  if (c !== null) {
    if (FALSE_VALUES.includes(c)) kioskChrome = false;
    else if (!TRUE_VALUES.includes(c)) bad('chrome', c, '1 or 0');
  }

  let theme: DashTheme | null = null;
  const t = q.get('theme');
  if (t !== null) {
    if ((THEMES as readonly string[]).includes(t)) theme = t as DashTheme;
    else bad('theme', t, THEMES.join(' | '));
  }

  let density: DashDensity | null = null;
  const n = q.get('density');
  if (n !== null) {
    if ((DENSITIES as readonly string[]).includes(n)) density = n as DashDensity;
    else bad('density', n, DENSITIES.join(' | '));
  }

  let range: number | null = null;
  const r = q.get('range');
  if (r !== null) {
    const seconds = parseRangeParam(r);
    if (seconds !== undefined) range = seconds;
    else bad('range', r, TIME_RANGE_PRESETS.map((p) => p.id).join(' | '));
  }

  let paused = false;
  const p = q.get('paused');
  if (p !== null) {
    if (p === '1') paused = true;
    else if (p !== '0') bad('paused', p, '1 or 0');
  }

  const link = parseAbsoluteParams(q.get('from'), q.get('to'));
  if (link.rejected) bad('from/to', `${link.rejected.from}..${link.rejected.to}`, `a zoned ISO 8601 window (${link.rejected.reason})`);

  // The playlist. Each unusable part is set aside and noted; the rest still plays.
  const rotateIssues: RotateIssue[] = [];
  let playlist: Playlist | undefined;
  const rotate = q.get('rotate');
  const everyRaw = q.get('every');
  let everySeconds = ROTATE_DEFAULT_SECONDS;
  if (everyRaw !== null) {
    const parsed = /^\d{1,6}$/.test(everyRaw) ? Number(everyRaw) : undefined;
    if (rotate === null) {
      rotateIssues.push({ kind: 'every-alone' });
      bad('every', everyRaw, 'a playlist (rotate) to apply to');
    } else if (parsed === undefined) {
      rotateIssues.push({ kind: 'every', value: everyRaw, reason: 'unparseable' });
      bad('every', everyRaw, 'whole seconds');
    } else if (parsed < ROTATE_MIN_SECONDS) {
      rotateIssues.push({ kind: 'every', value: everyRaw, reason: 'tooShort' });
      bad('every', everyRaw, `at least ${ROTATE_MIN_SECONDS} seconds`);
    } else if (parsed > ROTATE_MAX_SECONDS) {
      rotateIssues.push({ kind: 'every', value: everyRaw, reason: 'tooLong' });
      bad('every', everyRaw, `at most ${ROTATE_MAX_SECONDS} seconds`);
    } else {
      everySeconds = parsed;
    }
  }
  if (rotate !== null) {
    const tokens: string[] = [];
    for (const raw of rotate.split(',')) {
      const value = raw.trim();
      if (value === '') continue;
      if (isValidToken(value)) tokens.push(value);
      else {
        rotateIssues.push({ kind: 'token', value });
        bad('rotate', value, 'a dashboard token');
      }
    }
    if (tokens.length > ROTATE_MAX_BOARDS) {
      rotateIssues.push({ kind: 'too-many', dropped: tokens.length - ROTATE_MAX_BOARDS });
      tokens.length = ROTATE_MAX_BOARDS;
    }
    if (tokens.length >= ROTATE_MIN_BOARDS) {
      playlist = { tokens, everySeconds };
      kiosk = true;
      if (dashboard !== null) {
        rotateIssues.push({ kind: 'dashboard-ignored' });
        dashboard = null;
      }
    } else {
      // One board is just that board; none leaves the viewer where an unrotated link would.
      rotateIssues.push({ kind: 'too-few', usable: tokens.length });
      if (tokens.length === 1 && dashboard === null) dashboard = tokens[0];
    }
  }

  const got: DashParams = { dashboard, kiosk, kioskChrome, theme, density, range, paused };
  if (playlist) got.playlist = playlist;
  if (rotateIssues.length > 0) got.rotateIssues = rotateIssues;
  if (link.absolute) got.absolute = link.absolute;
  if (link.rejected) got.rejected = link.rejected;
  return got;
}
