// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An inheriting colour field must not look like a field set to black.
import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BrandingThemeFields, EMPTY_BRANDING_THEME } from './BrandingThemeFields';

afterEach(cleanup);

describe('BrandingThemeFields swatches', () => {
  it('shows the empty swatch for every inheriting colour and a real colour for a set one', () => {
    render(
      <BrandingThemeFields value={{ ...EMPTY_BRANDING_THEME, primary: '#336699' }} onChange={vi.fn()} />,
    );
    // Four colour fields; only primary is set.
    expect(screen.getAllByTestId('color-swatch-empty')).toHaveLength(3);
    const set = screen.getByRole('button', { name: 'Primary swatch' });
    expect(set.getAttribute('data-empty')).toBeNull();
  });
});
