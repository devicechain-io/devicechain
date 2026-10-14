// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The board readability gate (e2e/readability.e2e.ts): every committed sim board, drawn by the
// replay's renderer in a real browser, measured for clipped text and overlapping widgets.
//
// The page is served by THIS app's Vite dev server (e2e/page/gate.html is a dev-only entry; the
// built replay does not contain it). The dev server reads the workspace packages' dist, so the
// packages must be built first: `npm run test:readability` does that through its pre-hook, and
// CI has already built them by the time it gets here.

import { defineConfig, devices } from '@playwright/test';

const PORT = 5176;
const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: './e2e',
  testMatch: '*.e2e.ts',
  outputDir: './test-results',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // A readability finding is deterministic: a retry would only hide a flaky settle wait,
  // which is a defect in the gate to fix, not to ride out.
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
    url: `${BASE_URL}/e2e/page/gate.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
