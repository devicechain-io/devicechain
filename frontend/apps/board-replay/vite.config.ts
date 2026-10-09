// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import react from '@vitejs/plugin-react';
import { createReadStream, existsSync } from 'node:fs';
import path from 'node:path';
import { defineConfig, type Plugin } from 'vite';

// The replay is served from a static host as plain files, so its assets resolve relative to
// wherever the bundle is placed (`base: './'`) and the entry keeps a fixed name, `main.js`,
// for a page's module script to reference. Chunks and assets are hashed.
//
// In dev, two same-origin URLs are served from the working tree so the page runs without a
// copy of the data: the pinned board definition, and a recording named by REPLAY_RECORDING
// (a path to a board-recording JSON file). Nothing is copied or committed.
function devData(): Plugin {
  const board = path.resolve(import.meta.dirname, '../../testdata/sim-dashboards/sp-dashboard.json');
  return {
    name: 'board-replay-dev-data',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const file =
          req.url === '/dev-data/board.json'
            ? board
            : req.url === '/dev-data/recording.json'
              ? process.env.REPLAY_RECORDING
              : undefined;
        if (!file || !existsSync(file)) return next();
        res.setHeader('Content-Type', 'application/json');
        createReadStream(file).pipe(res);
      });
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [react(), devData()],
  server: { port: 5175 },
  build: {
    rollupOptions: {
      output: {
        entryFileNames: 'main.js',
        chunkFileNames: 'chunks/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
});
