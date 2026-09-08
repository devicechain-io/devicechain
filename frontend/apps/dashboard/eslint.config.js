// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ESLint flat config for the /dash viewer — its SOLE purpose is
// eslint-plugin-i18next's `no-literal-string` rule, the same gate the console has
// carried since the ADR-066 string-externalization sweep. It is deliberately NOT a
// general lint config (no style or correctness rules), so it never competes with tsc
// as the type/quality gate.
//
// 🔴 WHY THIS FILE EXISTS AT ALL — AND WHAT ITS ABSENCE COST. Until it landed, the
// CI step named "Lint (i18n no-literal-string gate)" ran `npm run lint --workspaces
// --if-present`, and apps/console was the only workspace with a `lint` script. So a
// gate whose name says "i18n" and whose log says it passed was checking exactly one
// of the two apps, and the other one had no i18n at all. The `--if-present` that made
// the command convenient is the same thing that made the hole invisible: a workspace
// with no script is silently skipped, which is indistinguishable in the log from a
// workspace that passed. Adding the script above is what enrols this app; nothing in
// CI needed changing.
//
// 🔴 AND KNOW WHAT IT STILL CANNOT SEE. The rule runs in `jsx-only` mode: it walks JSX
// text and attributes, so it catches prose typed into a component and is STRUCTURALLY
// BLIND to a string returned from a plain .ts module. load.ts's six error messages
// lived in exactly that blind spot, and turning this rule on would not have found one
// of them. What covers that file is its type — `LoadError` has no field prose can live
// in — not this config. Treat the lint as the floor, not the proof.
//
// Parser: @babel/eslint-parser, NOT typescript-eslint, for the reason the console's
// config records — typescript-eslint's estree throws a version guard against the
// repo's TypeScript 7. Babel strips types and emits an ESTree AST with no dependency
// on the installed tsc.
import i18next from 'eslint-plugin-i18next';
import i18nextDefaults from 'eslint-plugin-i18next/lib/options/defaults.js';
import reactHooks from 'eslint-plugin-react-hooks';
import babelParser from '@babel/eslint-parser';

// The rule REPLACES (does not merge) any option list it is given, so extend the
// plugin's own defaults rather than hand-copying them — that keeps the built-in
// excludes drift-free and layers this app's non-text noise on top.
//
// 🔴 THIS LIST IS SHORT ON PURPOSE, and the difference from the console's is the
// interesting part: the console excludes a long tail of shadcn/radix structural props
// because its JSX is built from a component kit. /dash has NO kit — it renders native
// elements with inline styles, which is part of what it demonstrates to an external
// embedder — so nearly every attribute here is either real user text or a native HTML
// enum. Copying the console's list wholesale would have excluded attributes this app
// does use for prose.
const EXTRA_ATTRS = [
  // Native form-control enums and browser hints, never display text.
  'type', 'autoComplete', 'spellCheck', 'rel', 'target', 'role',
];
const EXTRA_WORDS = ['^DeviceChain$'];

export default [
  {
    ignores: [
      'dist/**',
      // The catalogs and the i18n config ARE the translations.
      'src/i18n/**',
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
    // App.tsx carries a `react-hooks/exhaustive-deps` disable directive that is inert
    // here because the rule is off; without this, eslint would report it as an unused
    // disable directive and fail the gate on non-i18n grounds.
    linterOptions: { reportUnusedDisableDirectives: 'off' },
    languageOptions: {
      parser: babelParser,
      parserOptions: {
        // No .babelrc in this workspace — presets are supplied inline. Babel reads the
        // per-file name ESLint passes to pick .ts vs .tsx mode.
        requireConfigFile: false,
        babelOptions: {
          presets: ['@babel/preset-typescript', '@babel/preset-react'],
        },
      },
    },
    // react-hooks is registered ONLY so the pre-existing disable directive resolves to
    // a known rule instead of erroring "rule not found". Its rules stay OFF.
    plugins: { i18next, 'react-hooks': reactHooks },
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
