import { defineConfig } from 'vitest/config';

// The app's pure logic now lives (and is tested) in @devicechain/dashboards, so
// this app currently has no tests (`test` passes with --passWithNoTests). Config
// kept for when app-level tests return; node environment by default.
export default defineConfig({
  test: {
    environment: 'node',
  },
});
