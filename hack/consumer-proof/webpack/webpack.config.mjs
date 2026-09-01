// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An ordinary webpack 5 application: babel for TSX, style-loader for CSS,
// HtmlWebpackPlugin for the page. Nothing here is bent to accommodate the packages —
// that is the point. If a consumer ever needs an alias, a `resolve.fallback` or a custom
// rule to build `@devicechain/widgets`, the package is what needs fixing.
//
// 🔴 THIS ARM IS THE REASON P3 EXISTS. The map contract was changed for the benefit of
// bundlers nothing else in this repository tests; a Vite-only proof would certify the one
// bundler that already worked.

import path from 'node:path';
import { fileURLToPath } from 'node:url';
import HtmlWebpackPlugin from 'html-webpack-plugin';

const here = path.dirname(fileURLToPath(import.meta.url));

export default {
  mode: 'production',
  target: 'web',
  entry: {
    main: './src/app.tsx',
    // 🔴 THE WORKER IS A SECOND ENTRY, AND THAT IS THE WHOLE RECIPE. It is not a
    // stylistic choice — the two obvious alternatives are both broken, and both were
    // measured rather than reasoned about:
    //
    //   `new URL('maplibre-gl/dist/maplibre-gl-worker.mjs', import.meta.url)` — webpack
    //   treats the target as an ASSET and copies it verbatim. The copy still contains
    //   `import "./maplibre-gl-shared.mjs"`, that sibling is never emitted, and the
    //   worker dies on its own first line. This rig caught it showing a 200 on the
    //   worker URL, eight markers, ten tiles and zero console errors.
    //
    //   `new Worker(new URL(...))` — webpack DOES bundle a worker correctly this way,
    //   but it hands back a Worker, and MapLibre 6 exposes only `setWorkerUrl`. There is
    //   no `setWorkerClass`, so a Worker instance is not something we can give it.
    //
    // An entry gets what is actually needed: one self-contained ES module at a filename
    // the app can name. MapLibre loads it with `{ type: 'module' }`, and a webpack entry
    // bundle has no imports left to resolve.
    'maplibre-worker': 'maplibre-gl/dist/maplibre-gl-worker.mjs',
  },
  devtool: false,
  output: {
    path: path.join(here, 'dist'),
    // The worker needs a filename src/runtime.ts can name; everything else is hashed.
    filename: (data) =>
      data.chunk.name === 'maplibre-worker' ? 'maplibre-worker.js' : '[name].[contenthash].js',
    chunkFilename: '[name].[contenthash].js',
    // Absolute, so an emitted URL resolves from the page — and so the worker's own
    // chunk runtime never has to consult `document`, which does not exist inside it.
    publicPath: '/',
    clean: true,
  },
  resolve: { extensions: ['.tsx', '.ts', '.mjs', '.js'] },
  module: {
    rules: [
      {
        test: /\.[jt]sx?$/,
        exclude: /node_modules/,
        use: {
          loader: 'babel-loader',
          options: {
            presets: [['@babel/preset-react', { runtime: 'automatic' }], '@babel/preset-typescript'],
          },
        },
      },
      { test: /\.css$/, use: ['style-loader', 'css-loader'] },
    ],
  },
  plugins: [
    // `chunks: ['main']` matters: without it the page would also load the worker bundle
    // on the main thread, where it does nothing but cost 460 KB.
    new HtmlWebpackPlugin({ template: path.join(here, 'index.html'), chunks: ['main'] }),
  ],
  performance: { hints: false },
  stats: 'errors-warnings',
};
