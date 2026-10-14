// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's URL parameters, for a deep link a wall display or a bookmark can carry:
//
//   /dash/?dashboard=<token>&kiosk=1&theme=dark&density=wall
//
//   dashboard  the dashboard token to load right after sign-in
//   kiosk      1 | true  hide the chrome and offer fullscreen
//   theme      dark | light
//   density    compact | comfortable | wall
//
// A parameter that is absent is simply unset. One that is PRESENT BUT INVALID is ignored
// with a console warning: a typo in a bookmarked wall URL must still bring the board up
// (with the default for that one setting) rather than a blank screen nobody is there to read.

import { isValidToken } from '@devicechain/client';

export type DashTheme = 'dark' | 'light';
export type DashDensity = 'compact' | 'comfortable' | 'wall';

export interface DashParams {
  dashboard: string | null;
  kiosk: boolean;
  theme: DashTheme | null;
  density: DashDensity | null;
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

  return { dashboard, kiosk, theme, density };
}
