// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Extraction config for i18next-parser — the seam for ADR-066 sub-workstream (b),
// the one-time string-externalization sweep. It is NOT wired into CI yet (the
// sweep, an eslint no-bare-JSX-text rule, and this in a check step all land with
// b). Run it ad hoc with `npm run i18n:extract` to scaffold catalog keys from
// t()/<Trans> usages. Kept in sync with src/i18n/config.ts: same locales,
// default namespace, and the ':' namespace separator; keySeparator is off so
// keys stay flat semantic identifiers (login:signIn), never nested by dots.
export default {
  locales: ['en'],
  defaultNamespace: 'common',
  namespaceSeparator: ':',
  keySeparator: false,
  input: ['src/**/*.{ts,tsx}'],
  output: 'src/i18n/locales/$LOCALE/$NAMESPACE.json',
  sort: true,
  keepRemoved: true, // never drop an existing translation on a partial sweep
  createOldCatalogs: false,
  // Seed a new key with an empty string, so an unreviewed key is visibly blank
  // rather than silently echoing English into every locale.
  defaultValue: '',
};
