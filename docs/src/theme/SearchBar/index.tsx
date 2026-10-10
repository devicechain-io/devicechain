// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Wraps (does not eject) the search plugin's SearchBar to report searches to PostHog.
//
// The plugin's own component is rendered untouched; this only OBSERVES it from the
// outside, through DOM events bubbling out of it and a read of its dropdown:
//   - typing   -> `input` events on its text field
//   - results  -> the suggestion rows (or the "no results" row) in its dropdown
//   - a choice -> a click on a row, or Enter while a row is highlighted
// What is sent, and when, is decided in src/search/analytics.mjs (unit-tested); the client
// is the one static/analytics.js sets up on window.posthog.
//
// The dropdown's class names are CSS-module names (`suggestion_<hash>`), so rows are
// matched on the stable local-name prefix (src/search/selectors.mjs). The postbuild check
// scripts/check-search-selectors.mjs fails the build if a plugin upgrade renames them.
import React, { useEffect, useRef } from 'react';
import OriginalSearchBar from '@theme-original/SearchBar';
import type SearchBarType from '@theme/SearchBar';
import type { WrapperProps } from '@docusaurus/types';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import { useLocation } from '@docusaurus/router';
import { createSearchTracker, type PostHogLike } from '../../search/analytics.mjs';
import { CURSOR_CLASS, NO_RESULTS_CLASS, SUGGESTION_CLASS } from '../../search/selectors.mjs';

type Props = WrapperProps<typeof SearchBarType>;

const SUGGESTION = `[class*="${SUGGESTION_CLASS}"]`;
const NO_RESULTS = `[class*="${NO_RESULTS_CLASS}"]`;
const CURSOR = new RegExp(`(^|\\s)${CURSOR_CLASS}`);

function rows(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(SUGGESTION));
}

/** Result count as displayed, or null while the dropdown shows neither results nor "no results". */
function readResultCount(root: HTMLElement | null): number | null {
  if (!root) return null;
  const n = rows(root).length;
  if (n > 0) return n;
  return root.querySelector(NO_RESULTS) ? 0 : null;
}

// ONE tracker for the page's lifetime, not one per mount. The navbar (and with it this
// component) remounts when the reader moves between the search page and a doc, or between
// docs plugin instances; a per-mount tracker would lose a selection made just before the
// remount, and with it the click event. Each mount only re-points `live.root`.
const live: { root: HTMLElement | null; locale: string; path: string; lastPath: string | null } = {
  root: null,
  locale: 'en',
  path: '/',
  lastPath: null,
};
const tracker = createSearchTracker({
  getPostHog: () => (window as unknown as { posthog?: PostHogLike }).posthog,
  getLocale: () => live.locale,
  getPagePath: () => live.path,
  getResultCount: () => readResultCount(live.root),
});

export default function SearchBarWrapper(props: Props): React.JSX.Element {
  const {
    i18n: { currentLocale },
  } = useDocusaurusContext();
  const location = useLocation();
  const rootRef = useRef<HTMLDivElement>(null);
  live.locale = currentLocale;
  live.path = location.pathname;

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return undefined;
    live.root = root;

    const onInput = (event: Event) => {
      // Only a person typing: the plugin also writes the field programmatically (restoring
      // a highlight query after navigation), which must not count as a search. A
      // composition in progress (an IME building pinyin or kana) is not a query yet;
      // `compositionend` below reports its final text.
      if (!event.isTrusted || !(event.target instanceof HTMLInputElement)) return;
      if ((event as InputEvent).isComposing) return;
      tracker.input(event.target.value);
    };
    const onCompositionEnd = (event: Event) => {
      if (event.target instanceof HTMLInputElement) tracker.input(event.target.value);
    };
    const onClick = (event: MouseEvent) => {
      const row = (event.target as Element | null)?.closest<HTMLElement>(SUGGESTION);
      if (!row || !root.contains(row)) return;
      const rank = rows(root).indexOf(row) + 1;
      if (rank > 0) tracker.select(rank);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Enter' || event.isComposing || !(event.target instanceof HTMLInputElement)) {
        return;
      }
      const rank = rows(root).findIndex((row) => CURSOR.test(row.className)) + 1;
      if (rank > 0) tracker.select(rank);
    };

    // Capture phase: this must read the highlighted row before the plugin acts on the key
    // or click and navigates away.
    root.addEventListener('input', onInput, true);
    root.addEventListener('compositionend', onCompositionEnd, true);
    root.addEventListener('click', onClick, true);
    root.addEventListener('keydown', onKeyDown, true);
    return () => {
      root.removeEventListener('input', onInput, true);
      root.removeEventListener('compositionend', onCompositionEnd, true);
      root.removeEventListener('click', onClick, true);
      root.removeEventListener('keydown', onKeyDown, true);
      if (live.root === root) live.root = null;
    };
  }, []);

  // Every navigation: first settle a pending result click (the URL is the page we landed
  // on), then start a new page view for the once-per-query rule. A freshly mounted bar
  // runs this too, which is how a click survives the remount that follows it.
  useEffect(() => {
    tracker.navigated(location.pathname + location.hash);
    if (live.lastPath !== null && live.lastPath !== location.pathname) tracker.pageChanged();
    live.lastPath = location.pathname;
  }, [location.key, location.pathname, location.hash]);

  return (
    // `display: contents` keeps this wrapper out of the navbar's layout.
    <div ref={rootRef} style={{ display: 'contents' }}>
      <OriginalSearchBar {...props} />
    </div>
  );
}
