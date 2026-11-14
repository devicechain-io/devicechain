// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs after workspace tests have built the SDK; test:scripts stays dependency-free.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { createWidget } from './create-widget.mjs';
import { runSync, tscCommand } from './launch.mjs';

const frontend = fileURLToPath(new URL('../', import.meta.url));

test('generated package compiles, registers and renders the configured text', async (t) => {
  const root = mkdtempSync(path.join(frontend, 'node_modules', '.create-widget-'));
  t.after(() => {
    assert.ok(path.resolve(root).startsWith(path.resolve(frontend) + path.sep));
    rmSync(root, { recursive: true, force: true });
  });
  const target = path.join(root, 'widget');
  createWidget('acme/tank-level', target);
  const result = runSync(tscCommand(['-p', path.join(target, 'tsconfig.json')], { from: frontend }), {
    cwd: target, encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  const { widget } = await import(pathToFileURL(path.join(target, 'dist/index.js')).href);
  assert.doesNotThrow(() => createWidgetRegistry([widget]));
  const markup = renderToStaticMarkup(createElement(widget.implementation.component, {
    widget: { id: 'demo', type: 'acme/tank-level', options: { text: 'A real configured value' } },
    data: {},
  }));
  assert.ok(markup.includes('A real configured value'), markup);
});
