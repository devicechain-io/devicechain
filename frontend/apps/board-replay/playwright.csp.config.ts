// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The strict-CSP gate (e2e/csp.e2e.ts): the BUILT replay bundle, served by the spec's own static
// server under `style-src 'self'`. No webServer: the spec serves dist/ itself, so run
// `npm run test:csp`, which builds first.

import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testMatch: 'csp.e2e.ts',
  outputDir: './test-results/csp',
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list']],
  use: { ...devices['Desktop Chrome'], timezoneId: 'UTC', locale: 'en-US', reducedMotion: 'reduce' },
});
