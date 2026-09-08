// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [
    react(),
    // The shared index.html carries no script tag (see app/index.html): attaching an
    // entry to a page is bundler configuration, so each arm does it its own way.
    //
    // 🔴 `order: 'pre'` is load-bearing, and it cost a silent green to find out. Vite
    // discovers a build's entry points by SCANNING index.html, and a default-order
    // transform runs after that scan: the build then reported "3 modules transformed",
    // emitted an index.html with a script tag pointing at nothing, and exited 0. An
    // empty app would have satisfied every downstream stage that only asked "did it
    // build?" — which is why the driver asserts the bundle's SIZE and the rendered DOM,
    // not the build's exit status.
    {
      name: 'consumer-proof-entry',
      transformIndexHtml: {
        order: 'pre',
        handler: (html) =>
          html.replace('</body>', '  <script type="module" src="/src/app.tsx"></script>\n  </body>'),
      },
    },
  ],
  build: { outDir: 'dist', emptyOutDir: true },
});
