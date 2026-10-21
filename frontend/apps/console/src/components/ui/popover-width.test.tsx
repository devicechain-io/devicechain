// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The field dropdowns size themselves from Radix's custom properties. Tailwind v4 drops a bare
// `w-[--x]` (it compiles to `width: --x`), and the dropdown then grew past a phone screen, so this
// pins that the width classes the Combobox actually renders read the properties through var().
import '@/i18n/config';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { Combobox } from './combobox';
import { MultiSelect } from './multi-select';

afterEach(cleanup);

const popover = () => document.querySelector<HTMLElement>('[data-radix-popper-content-wrapper] > *')!;

function expectFieldWidth(el: HTMLElement) {
  const classes = el.className.split(/\s+/);
  expect(classes).toContain('w-[var(--radix-popover-trigger-width)]');
  expect(classes.some((c) => c.startsWith('max-w-[var(--radix-popover-content-available-width)'))).toBe(true);
  // No bare custom property, and not the Popover's fixed default either.
  expect(classes.filter((c) => /^(min-|max-)?w-\[--/.test(c))).toEqual([]);
  expect(classes).not.toContain('w-72');
}

describe('field dropdown width', () => {
  it('Combobox: as wide as its field, capped by the viewport', () => {
    render(<Combobox options={[{ value: 'a', label: 'A' }]} value="" onChange={() => {}} />);
    fireEvent.click(document.querySelector('[data-shot="combobox"]')!);
    expectFieldWidth(popover());
  });

  it('MultiSelect: the same', () => {
    render(<MultiSelect options={[{ value: 'a', label: 'A' }]} value={[]} onChange={() => {}} />);
    fireEvent.click(document.querySelector('button')!);
    expectFieldWidth(popover());
  });
});
