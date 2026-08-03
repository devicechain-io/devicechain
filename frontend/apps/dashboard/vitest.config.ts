// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vitest/config';

// Node environment: the app's only tested logic is loadDashboard, which parses text
// and touches no DOM, and the samples gate reads fixtures off disk. Rendering is
// covered where the widgets live, in @devicechain/widgets.
export default defineConfig({
  test: {
    environment: 'node',
  },
});
