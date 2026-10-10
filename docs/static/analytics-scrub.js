// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// URL scrubbing for PostHog events. Loaded (via `scripts` in docusaurus.config.ts) before
// /analytics.js, which installs scrubEvent as PostHog's `before_send`. In Node (the unit
// test) it is a CommonJS module; in the browser it sets window.dcAnalyticsScrub.
//
// WHY: the docs search bar keeps what someone typed in the URL (`/search/?q=<raw>`, and
// `?_highlight=<token>` on a result's address), and PostHog records the URL of every
// $pageview, $pageleave and autocapture click. That would send the raw query around the
// sanitiser in src/search/analytics.mjs, so the same URLs are cleaned here, at the one
// place every event passes through. Secret-style parameters are cleaned the same way,
// whatever page they appear on.
(function (root) {
  'use strict';

  var SENSITIVE_PARAMS = [
    'q',
    '_highlight',
    'token',
    'access_token',
    'refresh_token',
    'id_token',
    'api_key',
    'apikey',
    'key',
    'client_secret',
    'secret',
    'password',
    'passwd',
    'pwd',
    'auth',
    'code',
    'signature',
    'sig',
  ];

  function decode(s) {
    try {
      return decodeURIComponent(s.replace(/\+/g, ' '));
    } catch (e) {
      return s;
    }
  }

  function scrubPairs(s) {
    if (s === '') return s;
    return s
      .split('&')
      .filter(function (pair) {
        var eq = pair.indexOf('=');
        var name = decode(eq < 0 ? pair : pair.slice(0, eq)).toLowerCase();
        return SENSITIVE_PARAMS.indexOf(name) < 0;
      })
      .join('&');
  }

  /** Removes sensitive query / fragment parameters from a URL or path. Other input is returned as is. */
  function scrubUrl(url) {
    if (typeof url !== 'string' || (url.indexOf('?') < 0 && url.indexOf('#') < 0)) return url;
    var hashAt = url.indexOf('#');
    var head = hashAt < 0 ? url : url.slice(0, hashAt);
    var frag = hashAt < 0 ? '' : url.slice(hashAt + 1);
    var qAt = head.indexOf('?');
    var path = qAt < 0 ? head : head.slice(0, qAt);
    var query = qAt < 0 ? '' : scrubPairs(head.slice(qAt + 1));
    // Heading anchors have no `=`; a fragment with one is parameters (#access_token=...).
    if (frag.indexOf('=') >= 0) frag = scrubPairs(frag);
    return path + (query ? '?' + query : '') + (frag ? '#' + frag : '');
  }

  var URL_KEY = /(url|href|referrer|pathname)/i;
  var SEARCH_PAGE = /(^|\/)search\/?($|[?#])/;

  function scrubElement(el) {
    if (!el || typeof el !== 'object') return el;
    Object.keys(el).forEach(function (k) {
      if (typeof el[k] === 'string' && URL_KEY.test(k)) el[k] = scrubUrl(el[k]);
    });
    return el;
  }

  function scrubBag(bag) {
    if (!bag || typeof bag !== 'object') return;
    Object.keys(bag).forEach(function (k) {
      var v = bag[k];
      if (typeof v === 'string' && URL_KEY.test(k)) bag[k] = scrubUrl(v);
    });
    if (Array.isArray(bag.$elements)) bag.$elements.forEach(scrubElement);
    if (typeof bag.$elements_chain === 'string') {
      bag.$elements_chain = bag.$elements_chain.replace(
        /(attr__href|href)="([^"]*)"/g,
        function (_m, name, value) {
          return name + '="' + scrubUrl(value) + '"';
        },
      );
    }
  }

  /** PostHog `before_send`: cleans every URL-bearing property in place and returns the event. */
  function scrubEvent(event) {
    if (!event || !event.properties) return event;
    var props = event.properties;
    // The search results page puts the query in its document title.
    var url = typeof props.$current_url === 'string' ? props.$current_url : '';
    var path = typeof props.$pathname === 'string' ? props.$pathname : '';
    if (SEARCH_PAGE.test(url.split(/[?#]/)[0]) || SEARCH_PAGE.test(path)) {
      delete props.title;
      delete props.$title;
    }
    scrubBag(props);
    scrubBag(props.$set);
    scrubBag(props.$set_once);
    if (event.$set) scrubBag(event.$set);
    if (event.$set_once) scrubBag(event.$set_once);
    return event;
  }

  var api = { scrubUrl: scrubUrl, scrubEvent: scrubEvent, SENSITIVE_PARAMS: SENSITIVE_PARAMS };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.dcAnalyticsScrub = api;
})(typeof window !== 'undefined' ? window : this);
