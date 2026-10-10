// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// The class-name PREFIXES the search bar wrapper (src/theme/SearchBar) relies on to read
// the plugin's dropdown. The plugin's classes are CSS-module names (`suggestion_<hash>`),
// so the wrapper matches on the local name. scripts/check-search-selectors.mjs checks
// the built CSS still defines every one of these, so a plugin upgrade that renames them
// fails the build instead of silently dropping events.
export const SUGGESTION_CLASS = 'suggestion_';
export const NO_RESULTS_CLASS = 'noResults_';
export const CURSOR_CLASS = 'cursor_';
export const REQUIRED_CLASS_PREFIXES = [SUGGESTION_CLASS, NO_RESULTS_CLASS, CURSOR_CLASS];
