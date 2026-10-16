// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The console layout gate (e2e/layout.e2e.ts): dashboard-editor surfaces drawn by the console's
// own components in a real browser, at phone, tablet and desktop widths, in English and
// Spanish, measured for controls that are clipped or that sit on top of what they edit.
//
// The page is served by THIS app's Vite dev server (e2e/page/layout.html is a dev-only entry;
// the built console does not contain it). The dev server reads the workspace packages' dist, so
// the packages must be built first: `npm run test:layout` does that through its pre-hook, and
// CI has already built them by the time it gets here.

import { defineConfig, devices } from '@playwright/test';

const PORT = 5177;
const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: './e2e',
  testMatch: '*.e2e.ts',
  outputDir: './test-results',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // A layout finding is deterministic: a retry would only hide a flaky settle wait.
  retries: 0,
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: BASE_URL,
    deviceScaleFactor: 1,
    timezoneId: 'UTC',
    locale: 'en-US',
    reducedMotion: 'reduce',
  },
  webServer: {
    command: `npx vite --port ${PORT} --strictPort --host 127.0.0.1`,
    url: `${BASE_URL}/e2e/page/layout.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
