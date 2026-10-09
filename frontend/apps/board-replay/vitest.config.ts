// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

// jsdom: the app renders the real dashboard widgets. Dependencies are inlined-free; the
// built packages are consumed through their dist, exactly as the other apps do.
export default defineConfig({
  test: {
    environment: 'jsdom',
    // Process stylesheets (the replay's theme is imported `?inline` and tested as text).
    css: { include: [/.+/] },
    setupFiles: ['./vitest.setup.ts'],
  },
});
