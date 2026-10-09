// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ESLint flat config for the board-replay app: the i18next `no-literal-string` rule over
// JSX, the same gate /dash and the console carry. It is a copy of /dash's, because the apps
// ship independently, and for the same reason it is blind to prose built in a plain .ts
// module: load.ts returns error CODES for exactly that reason, and the catalogs render them.
// Parser: @babel/eslint-parser (typescript-eslint's estree rejects the repo's TypeScript 7).
import i18next from 'eslint-plugin-i18next';
import i18nextDefaults from 'eslint-plugin-i18next/lib/options/defaults.js';
import babelParser from '@babel/eslint-parser';

// The rule REPLACES (does not merge) an option list it is given, so extend the plugin's own
// defaults rather than hand-copying them.
const EXTRA_ATTRS = ['type', 'spellCheck', 'rel', 'target', 'role', 'data-testid', 'data-state'];
const EXTRA_WORDS = ['^DeviceChain$'];

export default [
  {
    ignores: [
      'dist/**',
      // The catalogs and the i18n config ARE the translations.
      'src/i18n/**',
      'scripts/**',
      '**/*.test.ts',
      '**/*.test.tsx',
      'vite.config.ts',
      'vitest.config.ts',
      'vitest.setup.ts',
      'eslint.config.js',
    ],
  },
  {
    files: ['src/**/*.{ts,tsx}'],
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
          words: {
            exclude: [...i18nextDefaults.words.exclude, ...EXTRA_WORDS],
          },
        },
      ],
    },
  },
];
