// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Bounded library correctness gate, independent of the apps' JSX i18n rules.
// Reuse the installed Babel parser: typescript-eslint is not in the lockfile
// and the apps already use Babel to parse the native TypeScript 7 sources.
// Scripts use --no-inline-config: existing React/TS rule directives are outside
// this gate, and inline directives must not suppress its no-debugger check.
import { createRequire } from 'node:module';

// npm installs Babel under the console workspace in the current lockfile.
// Resolve its existing tools there instead of adding root dependencies.
const require = createRequire(new URL('./apps/console/package.json', import.meta.url));
const babelParser = require('@babel/eslint-parser');

export default [
  { ignores: ['dist/**', 'node_modules/**'] },
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      parser: babelParser,
      parserOptions: {
        requireConfigFile: false,
        babelOptions: {
          presets: [require.resolve('@babel/preset-typescript')],
          overrides: [{ test: /\.tsx$/, presets: [require.resolve('@babel/preset-react')] }],
        },
      },
    },
    // Do not enable core no-unused-vars/no-undef on TypeScript. tsc remains
    // the type gate; this deliberately introduces no source-cleanup project.
    rules: { 'no-debugger': 'error' },
  },
];
