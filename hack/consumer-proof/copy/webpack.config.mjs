// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The COPY arm. Same application, same bundler as the `webpack` arm, one difference:
// the worker is not bundled at all, it is COPIED into the served output (copy-worker.mjs).
//
// 🔴 THIS ARM EXISTS BECAUSE THE PACKAGE README PUBLISHES THIS RECIPE. It is the answer
// for a host whose bundler has no worker-entry story — and it is the recipe that was
// WRONG on npmjs.com until this rig ran: it said to copy `maplibre-gl-worker.mjs`, full
// stop, and that file imports a sibling that then is not there. A published recipe
// nothing exercises is exactly how that happened, so it is exercised here.

import path from 'node:path';
import { fileURLToPath } from 'node:url';
import HtmlWebpackPlugin from 'html-webpack-plugin';

const here = path.dirname(fileURLToPath(import.meta.url));

export default {
  mode: 'production',
  target: 'web',
  entry: { main: './src/app.tsx' },
  devtool: false,
  output: {
    path: path.join(here, 'dist'),
    filename: '[name].[contenthash].js',
    chunkFilename: '[name].[contenthash].js',
    publicPath: '/',
    // NOT `clean`: copy-worker.mjs runs after webpack and its output would be the thing
    // cleaned away on the next build. The rig removes `dist` between builds instead.
    clean: false,
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
  plugins: [new HtmlWebpackPlugin({ template: path.join(here, 'index.html') })],
  performance: { hints: false },
  stats: 'errors-warnings',
};
