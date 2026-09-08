// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

// jsdom, not node. This app's tests used to be pure logic (loadDashboard parses text
// and touches no DOM) and ran under `node`; the i18n config does not — it installs a
// browser language detector, reads localStorage, and writes `<html lang>`. jsdom is a
// superset, so the samples gate and the host-wiring scan run under it unchanged.
//
// A per-file `@vitest-environment` docblock would have kept the rest on `node`, and
// was rejected for the reason the console's config records: it proved unreliable
// across Node versions in CI, which is a bad trade for a startup cost measured in
// milliseconds on a suite this size.
export default defineConfig({
  test: {
    environment: 'jsdom',
    // Force a working in-memory localStorage — see the setup file.
    setupFiles: ['./vitest.setup.ts'],
  },
});
