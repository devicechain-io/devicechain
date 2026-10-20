// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The boot sequence in main.tsx, run for real: it is the only thing standing between a Spanish
// operator and an English first paint, and nothing else exercises it — every other suite renders
// components directly, against catalogs vitest.setup.ts has already made resident.
//
// The app itself is stubbed down to one translated string (the real App and its providers need
// a backend); the i18n init, the loader seam and main.tsx's own ordering are the real ones.

import { act } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const LOCALE_KEY = 'dc.locale';

let releaseEs: () => void = () => {};

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  document.body.innerHTML = '<div id="root"></div>';
  // A fresh i18next: the external module is not reset by resetModules (see lazyCatalogs.test.tsx).
  vi.doMock('i18next', async (importOriginal) => {
    const real = await importOriginal<typeof import('i18next')>();
    return { ...real, default: real.default.createInstance() };
  });
  // The es chunk arrives only when the test says so.
  const gate = new Promise<void>((resolve) => {
    releaseEs = resolve;
  });
  vi.doMock('./i18n/catalogLoaders', () => ({
    LOADERS: { es: () => gate.then(() => import('./i18n/locales/es')) },
  }));
  vi.doMock('./App', async () => {
    const { useTranslation } = await import('react-i18next');
    return {
      default: function App() {
        const { t } = useTranslation('common');
        return <p data-testid="app">{t('back')}</p>;
      },
    };
  });
  const passThrough = ({ children }: { children: ReactNode }) => <>{children}</>;
  vi.doMock('@/auth/AuthProvider', () => ({ AuthProvider: passThrough }));
  vi.doMock('@/lib/capabilities', () => ({ AreaCapabilityProvider: passThrough }));
});

afterEach(async () => {
  for (const m of ['i18next', './i18n/catalogLoaders', './App', '@/auth/AuthProvider', '@/lib/capabilities']) {
    vi.doUnmock(m);
  }
  document.body.innerHTML = '';
});

const appText = () => document.querySelector('[data-testid="app"]')?.textContent ?? null;
const placeholder = () => document.querySelector('[data-testid="i18n-loading"]');

describe('main.tsx first render', () => {
  it('holds a Spanish start on a text-free placeholder until the catalog is in', async () => {
    localStorage.setItem(LOCALE_KEY, 'es');
    await act(async () => {
      await import('./main');
    });

    // The catalog has not arrived: no app (it would read "Back"), and no English on screen.
    expect(appText()).toBeNull();
    expect(placeholder()).not.toBeNull();
    expect(document.getElementById('root')?.textContent).toBe('');

    await act(async () => {
      releaseEs();
      const { i18nReady } = await import('./i18n/config');
      await i18nReady;
    });
    expect(appText()).toBe('Volver');
    expect(placeholder()).toBeNull();
  });

  it('renders an English start straight away, with no placeholder', async () => {
    localStorage.setItem(LOCALE_KEY, 'en');
    await act(async () => {
      await import('./main');
    });
    expect(placeholder()).toBeNull();
    expect(appText()).toBe('Back');
  });
});
