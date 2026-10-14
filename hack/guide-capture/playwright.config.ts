// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from '@playwright/test';

// The console the INSTANCE serves, never the Vite dev server: real ingress, real
// subscriptions, the product as shipped.
export const BASE_URL = process.env.GUIDE_BASE_URL ?? 'http://localhost';

export const LOCALES = ['en', 'es'] as const;

const frame = {
  baseURL: BASE_URL,
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  timezoneId: 'UTC',
  reducedMotion: 'reduce' as const,
  colorScheme: 'light' as const,
  channel: 'chrome', // the installed Chrome: no browser download
  // No traces, videos or failure screenshots, stated rather than left to the defaults: the
  // sessions in .auth/ are bearer tokens, and a trace records every request header and the
  // localStorage the init script writes. The guide's own shots are the only images taken.
  trace: 'off' as const,
  video: 'off' as const,
  screenshot: 'off' as const,
};

export default defineConfig({
  testDir: '.',
  testMatch: 'capture.spec.ts',
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  // One project per console locale; the context locale matches the console's so
  // browser-formatted dates agree with the UI language.
  projects: LOCALES.map((locale) => ({
    name: locale,
    use: { ...frame, locale },
  })),
});
