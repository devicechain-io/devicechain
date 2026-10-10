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
// matched on the stable local-name prefix. If a plugin upgrade renamed them, results would
// read as "unknown" and NOTHING would be sent, rather than a false zero-result search.
import React, { useEffect, useMemo, useRef } from 'react';
import OriginalSearchBar from '@theme-original/SearchBar';
import type SearchBarType from '@theme/SearchBar';
import type { WrapperProps } from '@docusaurus/types';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import { useLocation } from '@docusaurus/router';
import { createSearchTracker, type PostHogLike } from '../../search/analytics.mjs';

type Props = WrapperProps<typeof SearchBarType>;

const SUGGESTION = '[class*="suggestion_"]';
const NO_RESULTS = '[class*="noResults_"]';
const CURSOR = /(^|\s)cursor_/;

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

export default function SearchBarWrapper(props: Props): React.JSX.Element {
  const {
    i18n: { currentLocale },
  } = useDocusaurusContext();
  const location = useLocation();
  const rootRef = useRef<HTMLDivElement>(null);
  const live = useRef({ locale: currentLocale, path: location.pathname });
  live.current = { locale: currentLocale, path: location.pathname };

  const tracker = useMemo(
    () =>
      createSearchTracker({
        getPostHog: () => (window as unknown as { posthog?: PostHogLike }).posthog,
        getLocale: () => live.current.locale,
        getPagePath: () => live.current.path,
        getResultCount: () => readResultCount(rootRef.current),
      }),
    [],
  );

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return undefined;

    const onInput = (event: Event) => {
      // Only a person typing: the plugin also writes the field programmatically (restoring
      // a highlight query after navigation), which must not count as a search.
      if (!event.isTrusted || !(event.target instanceof HTMLInputElement)) return;
      tracker.input(event.target.value);
    };
    const onClick = (event: MouseEvent) => {
      const row = (event.target as Element | null)?.closest<HTMLElement>(SUGGESTION);
      if (!row || !root.contains(row)) return;
      const rank = rows(root).indexOf(row) + 1;
      if (rank > 0) tracker.select(rank);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Enter' || !(event.target instanceof HTMLInputElement)) return;
      const rank = rows(root).findIndex((row) => CURSOR.test(row.className)) + 1;
      if (rank > 0) tracker.select(rank);
    };

    // Capture phase: this must read the highlighted row before the plugin acts on the key
    // or click and navigates away.
    root.addEventListener('input', onInput, true);
    root.addEventListener('click', onClick, true);
    root.addEventListener('keydown', onKeyDown, true);
    return () => {
      root.removeEventListener('input', onInput, true);
      root.removeEventListener('click', onClick, true);
      root.removeEventListener('keydown', onKeyDown, true);
    };
  }, [tracker]);

  // Every navigation: first settle a pending result click (the URL is the page we landed
  // on), then start a new page view for the once-per-query rule.
  const previousPath = useRef(location.pathname);
  useEffect(() => {
    tracker.navigated(location.pathname + location.hash);
    if (previousPath.current !== location.pathname) {
      previousPath.current = location.pathname;
      tracker.pageChanged();
    }
  }, [tracker, location.key, location.pathname, location.hash]);

  useEffect(() => () => tracker.dispose(), [tracker]);

  return (
    // `display: contents` keeps this wrapper out of the navbar's layout.
    <div ref={rootRef} style={{ display: 'contents' }}>
      <OriginalSearchBar {...props} />
    </div>
  );
}
