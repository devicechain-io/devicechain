// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { ColorPicker } from './color-picker';

afterEach(cleanup);

describe('ColorPicker swatch', () => {
  it('shows the colour when one is set', () => {
    render(<ColorPicker ariaLabel="Primary" value="#336699" onChange={() => {}} />);
    const btn = screen.getByRole('button', { name: 'Primary' });
    expect(btn.getAttribute('data-empty')).toBeNull();
    expect(screen.queryByTestId('color-swatch-empty')).toBeNull();
    expect((btn.firstElementChild as HTMLElement).style.backgroundColor).toBe('rgb(51, 102, 153)');
  });

  it('shows an explicit empty state, not the black fallback, when the field inherits', () => {
    render(<ColorPicker ariaLabel="Primary" value="#000000" empty emptyLabel="inherit" onChange={() => {}} />);
    const btn = screen.getByRole('button', { name: 'Primary' });
    expect(btn.getAttribute('data-empty')).toBe('true');
    expect(btn.getAttribute('title')).toBe('inherit');
    const swatch = screen.getByTestId('color-swatch-empty');
    expect(swatch.style.backgroundColor).toBe('');
  });
});
