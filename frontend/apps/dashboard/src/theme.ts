// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's light/dark theme. theme.css defines the `.dark` token block; this is what
// puts the class on <html> (the widgets' chart-theme observer watches <html> for the swap).
//
// Precedence: a ?theme= parameter (a deployed wall URL says what it wants), then the
// viewer's own saved choice, then light. The parameter is never saved: a kiosk URL must
// not change what the same browser shows next time someone opens /dash without it.
// Clicking the toggle is a deliberate choice and is saved.

import { useCallback, useEffect, useState } from 'react';

import type { DashTheme } from './params';

const STORAGE_KEY = 'dc-dash-theme';

function readSaved(): DashTheme | null {
  try {
    const v = window.localStorage.getItem(STORAGE_KEY);
    return v === 'dark' || v === 'light' ? v : null;
  } catch {
    return null;
  }
}

function save(theme: DashTheme): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // storage unavailable: the choice holds for this page only
  }
}

export function applyTheme(theme: DashTheme): void {
  const root = document.documentElement;
  root.classList.toggle('dark', theme === 'dark');
  root.style.colorScheme = theme;
}

export function useTheme(fromParam: DashTheme | null): [DashTheme, () => void] {
  const [theme, setTheme] = useState<DashTheme>(() => fromParam ?? readSaved() ?? 'light');
  useEffect(() => applyTheme(theme), [theme]);
  const toggle = useCallback(() => {
    setTheme((cur) => {
      const next: DashTheme = cur === 'dark' ? 'light' : 'dark';
      save(next);
      return next;
    });
  }, []);
  return [theme, toggle];
}
