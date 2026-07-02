import { defineConfig } from 'vitest/config';

// Pure editor-model logic — no DOM needed, so the default node environment.
export default defineConfig({
  test: {
    environment: 'node',
  },
});
