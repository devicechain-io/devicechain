// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// One converted call site, driven end to end.
//
// 🔴 A GREEN PRIMITIVE TEST SAYS NOTHING ABOUT ITS CALLERS. SegmentedControl can be
// perfect while a caller passes the wrong props — most plausibly an icon-only picker
// that never sets `ariaLabel`, leaving three unnamed radios that look completely
// normal on screen. This is exactly that shape, so it is the site worth wiring up.
//
// The real i18n catalogs are loaded (not a mocked translator), so the assertions are
// on the strings an operator actually sees.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { ThemeProvider } from './ThemeProvider';
import { ThemeToggle } from './ThemeToggle';

afterEach(() => {
  cleanup();
  localStorage.clear();
});

function renderToggle() {
  render(
    <ThemeProvider defaultTheme="dark">
      <ThemeToggle />
    </ThemeProvider>,
  );
}

describe('ThemeToggle', () => {
  it('offers exactly the three themes, each with a name', () => {
    renderToggle();
    // Named individually rather than by counting: a count passes whether the three
    // are Light/Dark/System or three copies of the same label.
    for (const name of ['Light', 'Dark', 'System']) {
      expect(screen.getByRole('radio', { name })).toBeTruthy();
    }
    expect(screen.getAllByRole('radio')).toHaveLength(3);
  });

  // 🔴 THE DEFECT THIS SLICE FIXED. The old toggle rendered three icon buttons with
  // aria-label and title and NOTHING to say which one was in effect — the current
  // theme existed only as a background colour. This is the assertion that would have
  // failed against it.
  it('says which theme is in effect', () => {
    renderToggle();
    expect(screen.getByRole('radio', { name: 'Dark' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Light' }).getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('radio', { name: 'System' }).getAttribute('aria-checked')).toBe('false');
  });

  it('names the group, so the three are not announced loose on the page', () => {
    renderToggle();
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeTruthy();
  });

  it('switches the theme and moves the announced selection with it', () => {
    renderToggle();
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }));
    expect(screen.getByRole('radio', { name: 'Light' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('radio', { name: 'Dark' }).getAttribute('aria-checked')).toBe('false');
    // And it reached the provider, not just the control's own appearance — the whole
    // point of the click.
    expect(document.documentElement.classList.contains('dark')).toBe(false);
  });
});
