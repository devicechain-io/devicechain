// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ESLint flat config for the console — its SOLE purpose is the ADR-066
// sub-workstream (b) string-externalization sweep: eslint-plugin-i18next's
// `no-literal-string` rule enumerates every user-facing string literal still
// hard-coded in JSX so none is missed (the AdminUser popover slipping through the
// #524 chrome sweep is exactly the miss this rule prevents). It is deliberately
// NOT a general lint config — no style/correctness rules — so it never competes
// with tsc as the type/quality gate.
//
// Parser: @babel/eslint-parser, NOT typescript-eslint. typescript-eslint hard
// refuses to load against the repo's TypeScript 7 (the Go-based tsc) — its
// typescript-estree throws a version guard at import. Babel strips types and
// emits an ESTree AST with no dependency on the installed tsc, so it parses our
// TSX regardless of the TS version. The i18next rule only walks JSX text /
// attribute / literal nodes, so a type-free parse is sufficient.
import i18next from 'eslint-plugin-i18next';
import i18nextDefaults from 'eslint-plugin-i18next/lib/options/defaults.js';
import reactHooks from 'eslint-plugin-react-hooks';
import babelParser from '@babel/eslint-parser';

// The rule REPLACES (does not merge) any option list it is given, so we extend
// the plugin's own defaults rather than hand-copy them — this keeps the built-in
// excludes (htmlEntities, emoji, all-caps enum words, the i18n callees) intact
// and drift-free, and layers this codebase's non-text noise on top.
//
//  - jsx-attributes: shadcn/radix/lucide structural + enum props (variant, size,
//    side, …) are API values, never user text. Real user-text attributes
//    (placeholder/title/alt/label/aria-label/description) are deliberately NOT
//    excluded, so they stay in the worklist.
//  - callees: classname builders (cn/cva/clsx/…) carry tailwind fragments, and
//    navigate()/act() carry route paths / enum tokens — none are user text.
//  - words: the brand name is a proper noun, never localized.
const EXTRA_ATTRS = [
  'variant', 'size', 'side', 'sideOffset', 'align', 'alignOffset',
  'mode', 'axis', 'orientation', 'position', 'collapsible', 'banner',
  'color', 'fill', 'stroke', 'deviceColor', 'defaultTheme',
  'to', 'href', 'target', 'rel', 'role', 'name', 'htmlFor',
  'autoComplete', 'inputMode', 'dir', 'slot', 'data-testid',
  // radix/shadcn discriminants — Tabs/Select/RadioGroup `value`/`defaultValue`
  // are enum tokens that pick a pane/option, never display text (the visible
  // label is the element's children). Excluding them avoids forcing every such
  // literal to be hoisted into a named const just to dodge the rule.
  'value', 'defaultValue',
  // TokenField's `entityType` is the entity-kind key it resolves a token mask
  // from ('connector', 'role', 'metric-definition', …) — a technical
  // discriminant, never user text.
  'entityType',
];
const EXTRA_CALLEES = ['cn', 'cva', 'clsx', 'cx', 'twMerge', 'tv', 'navigate', 'act'];
const EXTRA_WORDS = ['^DeviceChain$'];

export default [
  {
    // Nothing here needs linting for bare strings:
    //  - gql/**            graphql-codegen output (regenerated, no UI text)
    //  - i18n/**           the catalogs + config ARE the translations
    //  - *.test.*          test fixtures legitimately hold literal strings
    //  - build/tool config not shipped to users
    ignores: [
      'dist/**',
      'src/gql/**',
      'src/i18n/**',
      '**/*.test.ts',
      '**/*.test.tsx',
      'vite.config.ts',
      'vitest.setup.ts',
      'codegen.ts',
      'i18next-parser.config.js',
      'eslint.config.js',
    ],
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    // The react-hooks/exhaustive-deps directives (see the plugins note below) are
    // inert here because that rule is off, so eslint would flag every one as an
    // "unused disable directive". This config lints for bare strings only — leave
    // those suppressions untouched.
    linterOptions: { reportUnusedDisableDirectives: 'off' },
    languageOptions: {
      parser: babelParser,
      parserOptions: {
        // No .babelrc in this workspace — presets are supplied inline. Babel
        // reads the per-file name ESLint passes to pick .ts vs .tsx mode.
        requireConfigFile: false,
        babelOptions: {
          presets: ['@babel/preset-typescript', '@babel/preset-react'],
        },
      },
    },
    // react-hooks is registered ONLY so the pre-existing
    // `// eslint-disable-next-line react-hooks/exhaustive-deps` directives across
    // the source resolve to a known rule instead of erroring "rule not found" —
    // which would fail the gate on non-i18n grounds. Its rules stay OFF: this
    // config lints for bare strings only.
    plugins: { i18next, 'react-hooks': reactHooks },
    rules: {
      // jsx-only flags both JSX text nodes and user-facing string-literal
      // attributes — the whole worklist for (b). Once the sweep is done this
      // reaches a true zero, so it can gate CI without false positives.
      'i18next/no-literal-string': [
        'error',
        {
          mode: 'jsx-only',
          'jsx-attributes': {
            exclude: [...i18nextDefaults['jsx-attributes'].exclude, ...EXTRA_ATTRS],
          },
          callees: {
            exclude: [...i18nextDefaults.callees.exclude, ...EXTRA_CALLEES],
          },
          words: {
            exclude: [...i18nextDefaults.words.exclude, ...EXTRA_WORDS],
          },
        },
      ],
    },
  },
];
