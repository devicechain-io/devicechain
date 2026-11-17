// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import i18n from 'i18next';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CommandWaitingNote } from '@/components/CommandWaitingNote';
import { formatDate, formatDateTime, formatNumber } from './format';
import { formatTime } from './utils';

const iso = '2030-06-17T13:24:56Z';

afterEach(async () => {
  cleanup();
  vi.restoreAllMocks();
  await i18n.changeLanguage('en');
});

describe('console formatting', () => {
  it('uses the selected language for dates, numbers and existing formatTime callers', async () => {
    for (const locale of ['en', 'es', 'en']) {
      await i18n.changeLanguage(locale);
      const date = new Date(iso);
      expect(formatDateTime(iso)).toBe(date.toLocaleString(locale));
      expect(formatDateTime(date)).toBe(date.toLocaleString(locale));
      expect(formatTime(iso)).toBe(date.toLocaleString(locale));
      expect(formatDate(iso)).toBe(date.toLocaleDateString(locale));
      expect(formatNumber(1234567.89)).toBe((1234567.89).toLocaleString(locale));
    }
  });

  it('uses the resolved catalog language rather than the requested regional tag', async () => {
    await i18n.changeLanguage('es-MX');
    expect(i18n.resolvedLanguage).toBe('es');
    const date = vi.spyOn(Date.prototype, 'toLocaleString').mockReturnValue('date');
    const number = vi.spyOn(Number.prototype, 'toLocaleString').mockReturnValue('number');
    expect(formatDateTime(iso)).toBe('date');
    expect(formatNumber(1234567)).toBe('number');
    expect(date).toHaveBeenCalledWith('es');
    expect(number).toHaveBeenCalledWith('es');
  });

  it('shows a placeholder for missing or invalid timestamps and supports translated fallbacks', () => {
    for (const value of [null, undefined, '', 'invalid', new Date(NaN)]) {
      expect(formatDateTime(value)).toBe('—');
      expect(formatDate(value)).toBe('—');
      expect(formatDateTime(value, 'unknown time')).toBe('unknown time');
    }
  });

  it('updates an already rendered timestamp when the operator switches language', async () => {
    await i18n.changeLanguage('en');
    render(<CommandWaitingNote command={{ status: 'QUEUED', dispatchAfter: iso }} />);
    expect(screen.getByTestId('command-waiting').textContent).toContain(new Date(iso).toLocaleString('en'));
    await act(async () => { await i18n.changeLanguage('es'); });
    expect(screen.getByTestId('command-waiting').textContent).toContain(new Date(iso).toLocaleString('es'));
  });
});
