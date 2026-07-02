// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

// React component tests need a DOM; the hooks/widgets render into jsdom. ECharts
// is mocked in the chart tests (jsdom has no canvas), so no browser is required.
export default defineConfig({
  test: {
    environment: 'jsdom',
  },
});
