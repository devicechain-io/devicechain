// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// Search analytics: WHAT is sent to PostHog about a docs search, and WHEN.
//
// Plain ESM with no imports, so `node --test` can run it (see analytics.test.mjs) and the
// search bar wrapper (src/theme/SearchBar) can bundle it. It never creates a PostHog
// client: it reuses the one static/analytics.js initialises on window.posthog, and
// therefore inherits whatever that file decides about WHERE it reports (the published
// host only) and about opt-out.

export const MAX_QUERY_LENGTH = 100;
export const DEFAULT_DEBOUNCE_MS = 1000;

const EMAIL = /[^\s@]+@[^\s@]+\.[^\s@]+/;
const JWT = /\beyJ[\w-]+\.[\w-]+\.[\w-]*/;
// "bearer <value>" where the value looks like a credential (has a digit), so the plain
// phrase "bearer token" or "bearer authentication" is still a searchable docs query.
const BEARER = /\bbearer\s+(?=[\w.~+/=-]*\d)[\w.~+/=-]{16,}/i;
// scheme://user[:pass]@host
const URL_USERINFO = /\b[a-z][a-z0-9+.-]*:\/\/[^\s/?#]*@/i;
// a secret passed in a query string
const URL_SECRET_PARAM =
  /[?&](?:access_token|token|api[_-]?key|apikey|key|secret|password|passwd|pwd|auth|signature|sig)=[^\s&]+/i;
const HEX_SECRET = /^[a-f0-9]{24,}$/i;
const SECRET_CHARSET = /^[A-Za-z0-9+/_=-]{24,}$/;

/** A single whitespace-delimited token that reads as a key rather than a word. */
function isSecretToken(token) {
  if (token.length < 24) return false;
  if (HEX_SECRET.test(token)) return true;
  // Base64 / base64url: random keys mix letter case and carry digits. Requiring both keeps
  // long hyphenated slugs and CamelCase identifiers (real docs queries) searchable.
  return (
    SECRET_CHARSET.test(token) && /\d/.test(token) && /[a-z]/.test(token) && /[A-Z]/.test(token)
  );
}

/**
 * True when a raw query looks like something that must never leave the browser.
 * Checked on the raw text (case-sensitive base64 would be damaged by lowercasing first)
 * and BEFORE the length cap (a secret must not survive by straddling the cut).
 */
export function looksSensitive(raw) {
  const text = String(raw);
  return (
    EMAIL.test(text) ||
    JWT.test(text) ||
    BEARER.test(text) ||
    URL_USERINFO.test(text) ||
    URL_SECRET_PARAM.test(text) ||
    text.split(/\s+/).some(isSecretToken)
  );
}

/**
 * Normalises a raw query for reporting.
 * Returns `{ redacted: true }` (no query at all) for anything sensitive, `null` for a
 * blank query (nothing to report), otherwise `{ redacted: false, query }` trimmed,
 * whitespace-collapsed, lowercased and capped at MAX_QUERY_LENGTH characters.
 */
export function sanitizeQuery(raw) {
  const trimmed = String(raw ?? '').trim();
  if (trimmed === '') return null;
  if (looksSensitive(trimmed)) return { redacted: true };
  const query = trimmed.replace(/\s+/g, ' ').toLowerCase().slice(0, MAX_QUERY_LENGTH);
  return { redacted: false, query };
}

function queryProps(sanitized) {
  return sanitized.redacted ? { redacted: true } : { query: sanitized.query, redacted: false };
}

/**
 * The reporting client, or null when nothing may be sent: PostHog absent, not initialised
 * (analytics.js initialises it on the published host only), or opted out.
 */
export function activePostHog(getPostHog) {
  const ph = getPostHog();
  if (!ph || typeof ph.capture !== 'function') return null;
  // `__loaded` is set by posthog.init(); before it the global is an inert stub.
  if (ph.__loaded !== true) return null;
  if (typeof ph.has_opted_out_capturing === 'function' && ph.has_opted_out_capturing()) {
    return null;
  }
  return ph;
}

/**
 * Decides when search events fire.
 *
 *  - `input(raw)`: the user typed. Restarts the debounce; nothing is sent per keystroke.
 *  - after `debounceMs` of quiet the search is reported once, with the result count read
 *    from the dropdown at that moment (`getResultCount()` returns null while the results
 *    are not yet showing, and the report waits rather than guess 0).
 *  - at most once per distinct query per page: `pageChanged()` starts a new page view.
 *  - `select(rank)`: a result was chosen. Reports the search immediately (if not yet
 *    reported), then `navigated(url)` reports the click with the URL actually navigated to.
 */
export function createSearchTracker({
  getPostHog,
  getLocale,
  getPagePath,
  getResultCount,
  debounceMs = DEFAULT_DEBOUNCE_MS,
  maxResultRetries = 3,
  setTimer = setTimeout,
  clearTimer = clearTimeout,
}) {
  let raw = '';
  let timer = null;
  let retries = 0;
  let pending = null; // a selected result waiting for its navigation
  let pendingTimer = null;
  let reported = new Set();

  const cancelTimer = () => {
    if (timer !== null) clearTimer(timer);
    timer = null;
  };

  const key = () => raw.trim().replace(/\s+/g, ' ').toLowerCase();

  /**
   * Reports the current query once; returns the result count when the results are on
   * screen (whether or not an event was sent), or null when they are not or nothing may
   * be sent.
   */
  function report() {
    const sanitized = sanitizeQuery(raw);
    if (!sanitized) return null;
    const ph = activePostHog(getPostHog);
    if (!ph) return null;
    const count = getResultCount();
    if (count === null || count === undefined) return null;
    const k = key();
    if (!reported.has(k)) {
      reported.add(k);
      ph.capture('docs_search', {
        ...queryProps(sanitized),
        locale: getLocale(),
        result_count: count,
        page_path: getPagePath(),
      });
    }
    return count;
  }

  function flush() {
    timer = null;
    if (reported.has(key())) return;
    if (report() === null && sanitizeQuery(raw) && retries < maxResultRetries) {
      // Results not on screen yet (index still loading): look again, a bounded number of times.
      retries += 1;
      timer = setTimer(flush, debounceMs);
    }
  }

  return {
    input(value) {
      raw = String(value ?? '');
      retries = 0;
      cancelTimer();
      if (raw.trim() === '') return;
      timer = setTimer(flush, debounceMs);
    },

    select(rank) {
      const sanitized = sanitizeQuery(raw);
      if (!sanitized) return;
      cancelTimer();
      const count = report();
      if (!activePostHog(getPostHog)) return;
      pending = { sanitized, rank, count, pagePath: getPagePath() };
      if (pendingTimer !== null) clearTimer(pendingTimer);
      // A selection that never navigates must not turn a later, unrelated page change
      // into a "click".
      pendingTimer = setTimer(() => {
        pending = null;
        pendingTimer = null;
      }, 3000);
    },

    navigated(url) {
      if (!pending) return;
      const p = pending;
      pending = null;
      if (pendingTimer !== null) clearTimer(pendingTimer);
      pendingTimer = null;
      const ph = activePostHog(getPostHog);
      if (!ph) return;
      const props = {
        ...queryProps(p.sanitized),
        locale: getLocale(),
        clicked_url: url,
        rank: p.rank,
        page_path: p.pagePath,
      };
      if (p.count !== null && p.count !== undefined) props.result_count = p.count;
      ph.capture('docs_search_result_clicked', props);
    },

    /** A new page view: the once-per-query memory starts over. */
    pageChanged() {
      reported = new Set();
      cancelTimer();
      raw = '';
    },

    dispose() {
      cancelTimer();
      if (pendingTimer !== null) clearTimer(pendingTimer);
    },
  };
}
