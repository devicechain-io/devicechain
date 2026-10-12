// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ESLint flat config for the host-widgets composition: the i18next `no-literal-string` rule
// over JSX, the same gate the apps that consume this package carry. A widget this package
// composes renders inside those apps, so prose hard-coded in its JSX would bypass their
// catalogs. Copied from board-replay's, because each workspace ships independently.
// Parser: @babel/eslint-parser (typescript-eslint's estree rejects the repo's TypeScript 7).
import i18next from 'eslint-plugin-i18next';
import i18nextDefaults from 'eslint-plugin-i18next/lib/options/defaults.js';
import babelParser from '@babel/eslint-parser';

// The rule REPLACES (does not merge) an option list it is given, so extend the plugin's own
// defaults rather than hand-copying them.
const EXTRA_ATTRS = ['type', 'spellCheck', 'rel', 'target', 'role', 'data-testid', 'data-state'];

export default [
  {
    ignores: ['dist/**', '**/*.test.ts', '**/*.test.tsx', 'eslint.config.js'],
  },
  {
    files: ['src/**/*.{ts,tsx}', 'fixtures/**/*.{ts,tsx}'],
    languageOptions: {
      parser: babelParser,
      parserOptions: {
        requireConfigFile: false,
        babelOptions: {
          presets: ['@babel/preset-typescript', '@babel/preset-react'],
        },
      },
    },
    plugins: { i18next },
    rules: {
      'i18next/no-literal-string': [
        'error',
        {
          mode: 'jsx-only',
          'jsx-attributes': {
            exclude: [...i18nextDefaults['jsx-attributes'].exclude, ...EXTRA_ATTRS],
          },
        },
      ],
    },
  },
];
